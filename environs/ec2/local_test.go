package ec2_test

import (
	"fmt"
	"io/ioutil"
	"launchpad.net/goamz/aws"
	amzec2 "launchpad.net/goamz/ec2"
	"launchpad.net/goamz/ec2/ec2test"
	"launchpad.net/goamz/s3/s3test"
	. "launchpad.net/gocheck"
	"launchpad.net/goyaml"
	"launchpad.net/juju/go/environs"
	"launchpad.net/juju/go/environs/ec2"
	"launchpad.net/juju/go/environs/jujutest"
	"launchpad.net/juju/go/testing"
	"launchpad.net/juju/go/version"
	"net/http"
	"strings"
)

var functionalConfig = []byte(`
environments:
  sample:
    type: ec2
    region: test
    control-bucket: test-bucket
    juju-origin: ppa
    access-key: x
    secret-key: x
`)

func registerLocalTests() {
	ec2.Regions["test"] = aws.Region{}
	envs, err := environs.ReadEnvironsBytes(functionalConfig)
	if err != nil {
		panic(fmt.Errorf("cannot parse functional tests config data: %v", err))
	}

	for _, name := range envs.Names() {
		Suite(&localServerSuite{
			Tests: jujutest.Tests{
				Environs: envs,
				Name:     name,
			},
		})
		Suite(&localLiveSuite{
			LiveTests: LiveTests{
				LiveTests: jujutest.LiveTests{
					Environs: envs,
					Name:     name,
				},
			},
		})
	}
}

// localLiveSuite runs tests from LiveTests using a fake
// EC2 server that runs within the test process itself.
type localLiveSuite struct {
	testing.LoggingSuite
	LiveTests
	srv localServer
	env environs.Environ
}

func (t *localLiveSuite) SetUpSuite(c *C) {
	ec2.UseTestImageData(true)
	t.srv.startServer(c)
	t.LiveTests.SetUpSuite(c)
	t.env = t.LiveTests.Env
	ec2.ShortTimeouts(true)
}

func (t *localLiveSuite) TearDownSuite(c *C) {
	t.LiveTests.TearDownSuite(c)
	t.srv.stopServer(c)
	t.env = nil
	ec2.ShortTimeouts(false)
	ec2.UseTestImageData(false)
}

// localServer represents a fake EC2 server running within
// the test process itself.
type localServer struct {
	ec2srv *ec2test.Server
	s3srv  *s3test.Server
}

func (srv *localServer) startServer(c *C) {
	var err error
	srv.ec2srv, err = ec2test.NewServer()
	if err != nil {
		c.Fatalf("cannot start ec2 test server: %v", err)
	}
	srv.s3srv, err = s3test.NewServer()
	if err != nil {
		c.Fatalf("cannot start s3 test server: %v", err)
	}
	ec2.Regions["test"] = aws.Region{
		EC2Endpoint: srv.ec2srv.URL(),
		S3Endpoint:  srv.s3srv.URL(),
	}
	srv.addSpice(c)
}

// addSpice adds some "spice" to the local server
// by adding state that may cause tests to fail.
func (srv *localServer) addSpice(c *C) {
	states := []amzec2.InstanceState{
		ec2test.ShuttingDown,
		ec2test.Terminated,
		ec2test.Stopped,
	}
	for _, state := range states {
		srv.ec2srv.NewInstances(1, "m1.small", "ami-a7f539ce", state, nil)
	}
}

func (srv *localServer) stopServer(c *C) {
	srv.ec2srv.Quit()
	srv.s3srv.Quit()
	// Clear out the region because the server address is
	// no longer valid.
	ec2.Regions["test"] = aws.Region{}
}

// localServerSuite contains tests that run against a fake EC2 server
// running within the test process itself.  These tests can test things that
// would be unreasonably slow or expensive to test on a live Amazon server.
// It starts a new local ec2test server for each test.  The server is
// accessed by using the "test" region, which is changed to point to the
// network address of the local server.
type localServerSuite struct {
	testing.LoggingSuite
	jujutest.Tests
	srv localServer
	env environs.Environ
}

func (t *localServerSuite) SetUpSuite(c *C) {
	ec2.UseTestImageData(true)
	t.Tests.SetUpSuite(c)
	ec2.ShortTimeouts(true)
}

func (t *localServerSuite) TearDownSuite(c *C) {
	t.Tests.TearDownSuite(c)
	ec2.ShortTimeouts(false)
	ec2.UseTestImageData(false)
}

func (t *localServerSuite) SetUpTest(c *C) {
	t.LoggingSuite.SetUpTest(c)
	t.srv.startServer(c)
	t.Tests.SetUpTest(c)
	t.env = t.Tests.Env
}

func (t *localServerSuite) TearDownTest(c *C) {
	t.Tests.TearDownTest(c)
	t.srv.stopServer(c)
	t.LoggingSuite.TearDownTest(c)
}

func (t *localServerSuite) TestBootstrapInstanceUserDataAndState(c *C) {
	err := t.env.Bootstrap(false)
	c.Assert(err, IsNil)

	// check that the state holds the id of the bootstrap machine.
	state, err := ec2.LoadState(t.env)
	c.Assert(err, IsNil)
	c.Assert(state.ZookeeperInstances, HasLen, 1)

	insts, err := t.env.Instances(state.ZookeeperInstances)
	c.Assert(err, IsNil)
	c.Assert(insts, HasLen, 1)
	c.Check(insts[0].Id(), Equals, state.ZookeeperInstances[0])

	info, err := t.env.StateInfo()
	c.Assert(err, IsNil)
	c.Assert(info, NotNil)

	// check that the user data is configured to start zookeeper
	// and the machine and provisioning agents.
	inst := t.srv.ec2srv.Instance(insts[0].Id())
	c.Assert(inst, NotNil)
	bootstrapDNS, err := insts[0].DNSName()
	c.Assert(err, IsNil)
	c.Assert(bootstrapDNS, Not(Equals), "")

	c.Logf("first instance: UserData: %q", inst.UserData)
	var x map[interface{}]interface{}
	err = goyaml.Unmarshal(inst.UserData, &x)
	c.Assert(err, IsNil)
	ec2.CheckPackage(c, x, "zookeeper", true)
	ec2.CheckPackage(c, x, "zookeeperd", true)
	ec2.CheckScripts(c, x, "juju-admin initialize", true)
	ec2.CheckScripts(c, x, "python -m juju.agents.provision", true)
	ec2.CheckScripts(c, x, "python -m juju.agents.machine", true)
	ec2.CheckScripts(c, x, fmt.Sprintf("JUJU_ZOOKEEPER='localhost%s'", ec2.ZkPortSuffix), true)
	ec2.CheckScripts(c, x, fmt.Sprintf("JUJU_MACHINE_ID='0'"), true)

	// check that a new instance will be started without
	// zookeeper, with a machine agent, and without a
	// provisioning agent.
	inst1, err := t.env.StartInstance(1, info)
	c.Assert(err, IsNil)
	inst = t.srv.ec2srv.Instance(inst1.Id())
	c.Assert(inst, NotNil)
	c.Logf("second instance: UserData: %q", inst.UserData)
	x = nil
	err = goyaml.Unmarshal(inst.UserData, &x)
	c.Assert(err, IsNil)
	ec2.CheckPackage(c, x, "zookeeperd", false)
	ec2.CheckPackage(c, x, "python-zookeeper", true)
	ec2.CheckScripts(c, x, "python -m juju.agents.machine", true)
	ec2.CheckScripts(c, x, "python -m juju.agents.provision", false)
	ec2.CheckScripts(c, x, fmt.Sprintf("JUJU_ZOOKEEPER='%s%s'", bootstrapDNS, ec2.ZkPortSuffix), true)
	ec2.CheckScripts(c, x, fmt.Sprintf("JUJU_MACHINE_ID='1'"), true)

	err = t.env.Destroy(append(insts, inst1))
	c.Assert(err, IsNil)

	_, err = ec2.LoadState(t.env)
	c.Assert(err, NotNil)
}

type toolsSpec struct {
	version string
	os      string
	arch    string
}

func toolsPath(vers, os, arch string) string {
	v, err := version.Parse(vers)
	if err != nil {
		panic(err)
	}
	return version.ToolsPathForVersion(v, os, arch)
}

var findToolsTests = []struct{
	major int
	os string
	arch string
	contents []string
	expect   string
	err      string
}{{
	version.Current.Major,
	version.CurrentOS,
	version.CurrentArch,
	[]string{version.ToolsPath},
	version.ToolsPath,
	"",
}, {
	1,
	"linux",
	"amd64",
	[]string{
		toolsPath("0.0.9", "linux", "amd64"),
	},
	"",
	"no compatible tools found",
}, {
	1,
	"linux",
	"amd64",
	[]string{
		toolsPath("2.0.9", "linux", "amd64"),
	},
	"",
	"no compatible tools found",
}, {
	1,
	"linux",
	"amd64",
	[]string{
		toolsPath("1.0.9", "linux", "amd64"),
		toolsPath("1.0.10", "linux", "amd64"),
		toolsPath("1.0.11", "linux", "amd64"),
	},
	toolsPath("1.0.11", "linux", "amd64"),
	"",
}, {
	1,
	"linux",
	"amd64",
	[]string{
		toolsPath("1.9.11", "linux", "amd64"),
		toolsPath("1.10.10", "linux", "amd64"),
		toolsPath("1.11.9", "linux", "amd64"),
	},
	toolsPath("1.11.9", version.CurrentOS, version.CurrentArch),
	"",
}, {
	1,
	"freebsd",
	"cell",
	[]string{
		toolsPath("1.9.9", "linux", "cell"),
		toolsPath("1.9.9", "freebsd", "amd64"),
		toolsPath("1.0.0", "freebsd", "cell"),
	},
	toolsPath("1.0.0", "freebsd", "cell"),
	"",
}}

func (t *localServerSuite) TestFindTools(c *C) {
	oldMajorVersion := *ec2.VersionCurrentMajor
	defer func() {
		*ec2.VersionCurrentMajor = oldMajorVersion
	}()
	for i, tt := range findToolsTests {
		c.Logf("test %d", i)
		*ec2.VersionCurrentMajor = tt.major
		for _, name := range tt.contents {
			err := t.env.PutFile(name, strings.NewReader(name))
			c.Assert(err, IsNil)
		}
		url, err := ec2.FindTools(t.env, &ec2.InstanceSpec{OS: tt.os, Arch: tt.arch})
		if tt.err != "" {
			c.Assert(err, ErrorMatches, tt.err)
		} else {
			c.Assert(err, IsNil)
			resp, err := http.Get(url)
			c.Assert(err, IsNil)
			data, err := ioutil.ReadAll(resp.Body)
			c.Assert(err, IsNil)
			c.Assert(string(data), Equals, tt.expect, Commentf("url %s", url))
		}
		t.env.Destroy(nil)
	}
}
