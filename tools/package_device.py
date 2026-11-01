"""Create a Windows-consumable flash package from actual IDF build metadata."""
import hashlib
import json
from pathlib import Path
import shutil
import sys
import tempfile
import datetime
import uuid

def package(build: Path, output: Path):
    build = build.resolve()
    metadata = json.loads((build / 'flasher_args.json').read_text())
    extra = metadata['extra_esptool_args']
    if extra['chip'] != 'esp32s3':
        raise ValueError('only esp32s3 firmware is supported')
    if output.exists():
        raise FileExistsError('choose a new output directory; existing packages are never overwritten')
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=output.parent) as tmp:
        staging = Path(tmp) / 'package'
        staging.mkdir()
        files = []
        for offset, name in metadata['flash_files'].items():
            address = int(offset, 0)
            if address < 0:
                raise ValueError('negative flash offset')
            source = (build / name).resolve()
            if not source.is_relative_to(build) or not source.is_file():
                raise ValueError('flash input outside build directory or missing')
            filename = f'{address:08x}-{source.name}'
            target = staging / filename
            shutil.copyfile(source, target)
            files.append({'offset': hex(address), 'file': filename,
                          'sha256': hashlib.sha256(target.read_bytes()).hexdigest()})
        if not files:
            raise ValueError('no flash images')
        manifest = {'version': 1, 'chip': extra['chip'],
                    'flash_settings': metadata['flash_settings'],
                    'files': files}
        (staging / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
        staging.rename(output)

if __name__ == '__main__':
    output = Path(sys.argv[2])
    if len(sys.argv) > 3 and sys.argv[3] == '--auto':
        output /= datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ-') + uuid.uuid4().hex[:8]
    package(Path(sys.argv[1]), output)
    print(output)
