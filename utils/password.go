// Copyright 2012, 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package utils

import (
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"io"
	"time"

	"launchpad.net/juju-core/thirdparty/pbkdf2"
)

var salt = []byte{0x75, 0x82, 0x81, 0xca}

// RandomBytes returns n random bytes.
func RandomBytes(n int) ([]byte, error) {
	buf := make([]byte, n)
	_, err := io.ReadFull(rand.Reader, buf)
	if err != nil {
		return nil, fmt.Errorf("cannot read random bytes: %v", err)
	}
	return buf, nil
}

// RandomPassword generates a random base64-encoded password.
func RandomPassword() (string, error) {
	b, err := RandomBytes(18)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// FastInsecureHash specifies whether a fast, insecure version of the hash
// algorithm will be used.  Changing this will cause PasswordHash to
// produce incompatible passwords.  It should only be changed for
// testing purposes - to make tests run faster.
var FastInsecureHash = false

// SlowPasswordHash returns base64-encoded one-way hash password that is
// computationally hard to crack by iterating through possible passwords.
func SlowPasswordHash(password string) string {
	iter := 8192
	if FastInsecureHash {
		iter = 1
	}
	// Generate 18 byte passwords because we know that MongoDB
	// uses the MD5 sum of the password anyway, so there's
	// no point in using more bytes. (18 so we don't get base 64
	// padding characters).
	startTime := time.Now()
	h := pbkdf2.Key([]byte(password), salt, iter, 18, sha512.New)
	hashed := base64.StdEncoding.EncodeToString(h)
	logger.Debugf("SlowPasswordHash(pbkdf2.Key) in %s", time.Since(startTime))
	return hashed
}

// PasswordHash returns base64-encoded one-way hash password that is
// computationally hard to crack by iterating through possible passwords.
func PasswordHash(password string) string {
	startTime := time.Now()
	sum := sha512.New()
	sum.Write([]byte(password))
	h := make([]byte, 0, sum.Size())
	h = sum.Sum(h)
	hashed := base64.StdEncoding.EncodeToString(h[:18])
	logger.Debugf("PasswordHash(pbkdf2.Key) in %s", time.Since(startTime))
	return hashed
}
