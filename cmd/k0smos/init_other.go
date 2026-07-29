//go:build !linux

package main

import (
	"context"
	"errors"
)

func run(context.Context) error { return errors.New("k0smos runs on linux only") }
