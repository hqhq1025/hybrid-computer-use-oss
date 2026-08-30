#!/usr/bin/env python3
"""Provider V2 process entrypoint for the Linux implementation."""

from native.linux.provider_v1.wire_v2 import main  # pyright: ignore[reportImplicitRelativeImport]

if __name__ == "__main__":
    main()
