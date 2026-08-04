//go:build !linux

package main

func maybeCgroupTrampoline() error { return nil }
