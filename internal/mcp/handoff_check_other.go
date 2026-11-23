//go:build !unix

package mcp

func handoffBinaryOK(string) error { return nil }
