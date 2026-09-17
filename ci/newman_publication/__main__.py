"""CLI печатает только закрытый результат; исходные исключения остаются приватны."""
from __future__ import annotations

import argparse
import sys
import zipfile
import zlib

from .common import Refusal, encode, load_limits, os_code
from .publication import check, project


class Parser(argparse.ArgumentParser):
    def error(self, message):
        raise Refusal("MALFORMED_INPUT")

    def exit(self, status=0, message=None):
        raise Refusal("MALFORMED_INPUT")


def main(argv=None):
    argv = sys.argv[1:] if argv is None else argv
    operation = argv[0] if argv and argv[0] in ("project", "check") else "project"
    result = {"schema_version": 1, "operation": operation, "status": "NOT_EXECUTED",
              "code": "INTERNAL_ERROR", "files_declared": 0, "files_checked": 0,
              "fields_checked": 0, "findings": 0, "archive_bytes": 0, "archive_sha256": None}
    try:
        parser = Parser(add_help=False, allow_abbrev=False)
        commands = parser.add_subparsers(dest="operation", required=True)
        for name in ("project", "check"):
            command = commands.add_parser(name, add_help=False, allow_abbrev=False)
            command.add_argument("--limits")
            command.add_argument("--manifest", required=True)
            if name == "project":
                command.add_argument("--output-dir", required=True)
            else:
                command.add_argument("--archive", required=True)
        args = parser.parse_args(argv)
        limits = load_limits(args.limits)
        if args.operation == "project":
            project(args.manifest, args.output_dir, limits, result)
        elif args.operation == "check":
            check(args.manifest, args.archive, sys.stdin.buffer, limits, result)
    except Refusal as error:
        result.update(status="NOT_EXECUTED", code=error.code)
    except OSError as error:
        result.update(status="NOT_EXECUTED", code=os_code(error))
    except (ValueError, zipfile.BadZipFile, zlib.error, RecursionError):
        result.update(status="NOT_EXECUTED", code="MALFORMED_INPUT")
    except KeyboardInterrupt:
        result.update(status="NOT_EXECUTED", code="INTERRUPTED")
    except Exception:
        result.update(status="NOT_EXECUTED", code="INTERNAL_ERROR")
    sys.stdout.buffer.write(encode(result))
    return {"CLEAN": 0, "FINDING": 1, "NOT_EXECUTED": 3}[result["status"]]


if __name__ == "__main__":
    raise SystemExit(main())
