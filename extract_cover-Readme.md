Yep. The logic is:

1. Scan a folder for `.cbr` and `.cbz`.
2. Sort the archive filenames alphabetically, case-insensitive.
3. Take the first archive.
4. Read its contents without extracting everything.
5. Find image files (`jpg`, `jpeg`, `png`, `webp`, `gif`, `bmp`, `avif`).
6. Sort those image filenames alphabetically.
7. Extract only the first one.
8. Rename it to `_cover.<original extension>` in the same folder as the archive.

For `.cbz`, Python handles it natively. For `.cbr`, this uses the `rarfile` package, which in turn needs a RAR backend such as `UnRAR` or `7-Zip`.


### Example

Given:

```text
D:\Manga\A Man's Man\
├── A Man's Man 001-100.cbz
├── A Man's Man 101-173.cbz
```

and inside `A Man's Man 001-100.cbz`:

```text
[0000] S002 - Chapter 1_001_.webp
[0000] S002 - Chapter 1_002_.webp
[0000] S002 - Chapter 1_003_.webp
...
```

the result becomes:

```text
D:\Manga\A Man's Man\
├── A Man's Man 001-100.cbz
├── A Man's Man 101-173.cbz
└── _cover.webp
```

The script does **not** assume `_000.jpg`, `[0000]`, chapter numbering, or any particular filename structure. It literally picks the alphabetically first image entry inside the alphabetically first archive.

### Installing CBR support

For CBZ, nothing extra is needed.

For CBR:

```bat
py -m pip install rarfile
```

You'll also need a RAR extraction backend. If you already have 7-Zip installed, `rarfile` can usually use it. On Windows, this is generally the least annoying route.

### Run it

From the manga folder:

```bat
py extract_cover.py
```

Or specify a folder:

```bat
py extract_cover.py "D:\Manga\A Man's Man"
```

One small caveat: **"alphabetically first archive" means normal filename sorting**, so `A Man's Man 001-100.cbz` will beat `A Man's Man 101-173.cbz`, as expected. [Verified logic, code-level]

If you actually want this to **process every subfolder automatically**, such as:

```text
Manga\
├── A Man's Man\
├── Another Manga\
├── Blah Blah\
...
```

and generate `_cover.*` in each one, that's a trivial modification and arguably more useful for a manga library.
