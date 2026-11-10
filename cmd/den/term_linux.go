package main

import "syscall"

// getTermios is the ioctl that reads a terminal's settings.
const getTermios = syscall.TCGETS
