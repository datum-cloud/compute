//go:build !linux

// SPDX-License-Identifier: AGPL-3.0-only

package main

import "errors"

func becomeSubreaper() error { return errors.New("shell sessions require Linux") }
