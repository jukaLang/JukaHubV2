#!/usr/bin/python3
import sys

from grpc_tools import protoc

raise SystemExit(protoc.main(["protoc"] + [argument for argument in sys.argv[1:] if argument]))
