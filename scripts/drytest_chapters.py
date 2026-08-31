#!/usr/bin/env python3
"""Dry test: chapter analysis for every folder + WC match for a sample."""
import sys, time
import os
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import populate_komik as pk
from pathlib import Path

LIB = pk.LIB
folders = sorted([p for p in LIB.iterdir() if p.is_dir()], key=lambda p: p.name.lower())
print(f"folders: {len(folders)}")

stats = {"archives": 0, "subfolders": 0, "volumes-only": 0, "no chapter info": 0}
odd = []
for f in folders:
    ch, src = pk.analyze_folder(f)
    stats[src] = stats.get(src, 0) + 1
    if ch is None or src == "volumes-only":
        odd.append((f.name, ch, src))

print(stats)
print("\nfolders with no/weak chapter info:")
for n, ch, src in odd:
    print(f"  {ch} [{src}] {n}")

# print a few parsed values for sanity
print("\nsample parses:")
import random
random.seed(7)
for f in random.sample(folders, 12):
    print(f"  {pk.analyze_folder(f)[0]} <= {f.name}")
