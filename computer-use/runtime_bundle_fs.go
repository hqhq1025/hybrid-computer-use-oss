package main

import "embed"

//go:embed runtime.py native/__init__.py native/linux/__init__.py native/linux/provider/*.py
var linuxRuntimeFS embed.FS
