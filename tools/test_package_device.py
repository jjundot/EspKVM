import json
from pathlib import Path
import tempfile
import unittest
from package_device import package

class PackageTests(unittest.TestCase):
    def test_metadata_and_hash(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            build = root / 'build'
            build.mkdir()
            (build / 'app.bin').write_bytes(b'firmware')
            data = {'extra_esptool_args': {'chip': 'esp32s3'},
                    'flash_files': {'0x10000': 'app.bin'},
                    'flash_settings': {'flash_mode': 'dio', 'flash_freq': '80m', 'flash_size': '2MB'}}
            (build / 'flasher_args.json').write_text(json.dumps(data))
            output = root / 'flash'
            package(build, output)
            manifest = json.loads((output / 'manifest.json').read_text())
            self.assertEqual(manifest['files'][0]['offset'], '0x10000')
            self.assertEqual((output / manifest['files'][0]['file']).read_bytes(), b'firmware')
            with self.assertRaises(FileExistsError):
                package(build, output)
            data['flash_files'] = {'0x10000': '../outside.bin'}
            (root / 'outside.bin').write_bytes(b'outside')
            (build / 'flasher_args.json').write_text(json.dumps(data))
            with self.assertRaises(ValueError):
                package(build, root / 'invalid')
            self.assertFalse((root / 'invalid').exists())

if __name__ == '__main__':
    unittest.main()
