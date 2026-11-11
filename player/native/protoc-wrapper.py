#!/usr/bin/python3
# The protoc that chiaki-ng's build runs for nanopb: grpcio-tools' bundled one.
#
# grpc_tools.protoc.main() adds protobuf's well-known types (the directory with
# google/protobuf/descriptor.proto, which nanopb.proto imports) only when it is
# run as "python -m grpc_tools.protoc"; called as a function, as here, it does
# not, so that include directory is added unless the caller already passes it.
import os
import sys

import grpc_tools
from grpc_tools import protoc

arguments = [argument for argument in sys.argv[1:] if argument]
well_known = os.path.join(os.path.dirname(os.path.abspath(grpc_tools.__file__)), "_proto")
if os.path.isdir(well_known) and not any(well_known in argument for argument in arguments):
    arguments.append("-I" + well_known)

raise SystemExit(protoc.main(["protoc"] + arguments))
