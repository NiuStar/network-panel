#!/usr/bin/env python3
"""Create the deterministic, tracked-only panel-runtime-scripts.zip asset."""
import argparse
import os
from pathlib import Path
import re
import stat
import subprocess
import tempfile
import zipfile


def package(root, output):
    tracked = subprocess.run(
        ['git', '-C', str(root), 'ls-files', '--cached', '--stage', '-z', '--', 'install.sh', 'easytier'],
        check=True, capture_output=True,
    ).stdout
    names = []
    for entry in tracked.split(b'\0'):
        if not entry:
            continue
        metadata, raw_name = entry.split(b'\t', 1)
        mode, _object, stage = metadata.split()
        name = raw_name.decode('utf-8')
        if mode not in (b'100644', b'100755') or stage != b'0':
            raise ValueError(f'not a regular tracked file: {name}')
        names.append(name)
    names.sort()
    if 'install.sh' not in names:
        raise ValueError('tracked install.sh is required')
    if not any(name.startswith('easytier/') for name in names):
        raise ValueError('tracked easytier files are required')
    resolved_output = output.resolve()
    if resolved_output == (root / 'install.sh').resolve() or resolved_output.is_relative_to(
            (root / 'easytier').resolve()):
        raise ValueError('output overlaps runtime sources')
    for name in names:
        parts = name.split('/')
        if (name != 'install.sh' and not name.startswith('easytier/')) or any(
                part in ('', '.', '..') or '\\' in part or ':' in part
                or any(ord(char) < 32 or ord(char) == 127 for char in part)
                for part in parts):
            raise ValueError(f'unsafe runtime path: {name!r}')
        path = root
        for part in name.split('/'):
            path = path / part
            if path.is_symlink():
                raise ValueError(f'symlink is forbidden: {name}')
        if not stat.S_ISREG(path.lstat().st_mode):
            raise ValueError(f'not a regular file: {name}')
        for part in name.lower().split('/'):
            if (part == '.env' or part.startswith('.env.') or part.endswith('.env')
                    or re.search(r'\.(?:db|sqlite3?|sql)(?:$|[.-])', part)):
                raise ValueError(f'forbidden runtime file: {name}')
    output.parent.mkdir(parents=True, exist_ok=True)
    # Publish only a complete archive; a failed build must not truncate an old asset.
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(dir=output.parent, prefix='.runtime-', suffix='.zip',
                                         delete=False) as stream:
            temporary = Path(stream.name)
            with zipfile.ZipFile(stream, 'w', compression=zipfile.ZIP_STORED) as archive:
                for name in names:
                    info = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
                    info.create_system = 3
                    mode = 0o755 if name.endswith('.sh') else 0o644
                    info.external_attr = (stat.S_IFREG | mode) << 16
                    archive.writestr(info, (root / name).read_bytes())
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, output)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    package(args.root, args.output)


if __name__ == '__main__':
    main()
