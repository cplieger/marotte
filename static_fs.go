//go:build !marotte_test

package main

import "io/fs"

func appStatic() fs.FS { return staticFS }
