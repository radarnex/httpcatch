//go:build !windows

package sinks

import "syscall"

const noFollowFlag = syscall.O_NOFOLLOW
