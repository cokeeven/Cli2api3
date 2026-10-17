//go:build !windows

package main

func nativeWorkerJob() (func(), error) { return func() {}, nil }
