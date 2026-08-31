#!/usr/bin/env python3

from pathlib import Path
import sys
import zipfile

try:
    import rarfile
except ImportError:
    rarfile = None


IMAGE_EXTENSIONS = {
    ".jpg",
    ".jpeg",
    ".png",
    ".webp",
    ".gif",
    ".bmp",
    ".avif",
}


def is_image(filename: str) -> bool:
    return Path(filename).suffix.lower() in IMAGE_EXTENSIONS


def get_first_archive(folder: Path) -> Path | None:
    archives = [
        p for p in folder.iterdir()
        if p.is_file() and p.suffix.lower() in {".cbr", ".cbz"}
    ]

    archives.sort(key=lambda p: p.name.lower())

    return archives[0] if archives else None


def get_first_image_from_cbz(archive: Path):
    with zipfile.ZipFile(archive, "r") as zf:
        images = [
            info
            for info in zf.infolist()
            if not info.is_dir() and is_image(info.filename)
        ]

        images.sort(key=lambda x: x.filename.lower())

        return images[0] if images else None


def get_first_image_from_cbr(archive: Path):
    if rarfile is None:
        raise RuntimeError(
            "The 'rarfile' package is required for .cbr files.\n"
            "Install it with:\n"
            "    py -m pip install rarfile"
        )

    with rarfile.RarFile(archive, "r") as rf:
        images = [
            info
            for info in rf.infolist()
            if not info.is_dir() and is_image(info.filename)
        ]

        images.sort(key=lambda x: x.filename.lower())

        return images[0] if images else None


def extract_cover(archive: Path):
    extension = None

    if archive.suffix.lower() == ".cbz":
        image = get_first_image_from_cbz(archive)

        if image is None:
            print(f"[!] No image found in: {archive.name}")
            return False

        extension = Path(image.filename).suffix.lower()
        output = archive.parent / f"_cover{extension}"

        print(f"[+] Archive : {archive.name}")
        print(f"[+] Image   : {image.filename}")
        print(f"[+] Output  : {output.name}")

        with zipfile.ZipFile(archive, "r") as zf:
            with zf.open(image) as src, open(output, "wb") as dst:
                dst.write(src.read())

    elif archive.suffix.lower() == ".cbr":
        image = get_first_image_from_cbr(archive)

        if image is None:
            print(f"[!] No image found in: {archive.name}")
            return False

        extension = Path(image.filename).suffix.lower()
        output = archive.parent / f"_cover{extension}"

        print(f"[+] Archive : {archive.name}")
        print(f"[+] Image   : {image.filename}")
        print(f"[+] Output  : {output.name}")

        with rarfile.RarFile(archive, "r") as rf:
            with rf.open(image) as src, open(output, "wb") as dst:
                dst.write(src.read())

    else:
        print(f"[!] Unsupported archive: {archive}")
        return False

    print("[+] Done.")
    return True


def main():
    if len(sys.argv) > 1:
        folder = Path(sys.argv[1]).expanduser().resolve()
    else:
        folder = Path.cwd()

    if not folder.is_dir():
        print(f"[!] Not a folder: {folder}")
        sys.exit(1)

    archive = get_first_archive(folder)

    if archive is None:
        print(f"[!] No .cbr or .cbz files found in: {folder}")
        sys.exit(1)

    try:
        success = extract_cover(archive)
    except Exception as e:
        print(f"[!] Error: {e}")
        sys.exit(1)

    sys.exit(0 if success else 1)


if __name__ == "__main__":
    main()