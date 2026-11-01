"""Locked generation; publish complete files only, preserve timestamps for equal output."""
import contextlib
import importlib.metadata
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]

@contextlib.contextmanager
def lock(path):
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open('a+b') as stream:
        if os.name == 'nt':
            import msvcrt
            stream.write(b'0'); stream.flush(); stream.seek(0)
            msvcrt.locking(stream.fileno(), msvcrt.LK_LOCK, 1)
        else:
            import fcntl
            fcntl.flock(stream, fcntl.LOCK_EX)
        yield

def main():
    kind, target, plugin = sys.argv[1:]
    target = Path(target)
    dest = target.parent if kind == 'go' else target
    dest.mkdir(parents=True, exist_ok=True)
    expected = {'grpcio-tools': '1.71.0', 'protobuf': '5.29.6'}
    if kind == 'c':
        expected['nanopb'] = '0.4.9.1'
    for package, version in expected.items():
        if importlib.metadata.version(package) != version:
            raise RuntimeError('incorrect generator dependency version: ' + package)
    with lock(ROOT / 'out' / ('proto-' + kind + '.lock')):
        with tempfile.TemporaryDirectory(dir=dest) as tmp:
            args = [sys.executable, '-m', 'grpc_tools.protoc', '-I' + str(ROOT / 'sources/protocol')]
            if kind == 'go':
                version = subprocess.check_output([plugin, '--version'], text=True).strip()
                if version != 'protoc-gen-go v1.36.6':
                    raise RuntimeError('incorrect protoc-gen-go version: ' + version)
                args += ['--plugin=protoc-gen-go=' + plugin, '--go_out=' + tmp, '--go_opt=paths=source_relative']
            else:
                args += ['--plugin=protoc-gen-nanopb=' + plugin, '--nanopb_out=-I' + str(ROOT / 'sources/protocol') + ':' + tmp]
            subprocess.run(args + [str(ROOT / 'sources/protocol/ek.proto')], check=True)
            for source in Path(tmp).iterdir():
                output = dest / source.name
                if not output.exists() or output.read_bytes() != source.read_bytes():
                    os.replace(source, output)

if __name__ == '__main__':
    main()
