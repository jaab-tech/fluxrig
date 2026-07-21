import os

def main():
    pkg_dir = "/Users/andresa/git/fluxrig/pkg"
    for root, dirs, files in os.walk(pkg_dir):
        # check if there are .go files
        if any(f.endswith(".go") for f in files):
            pkg_name = os.path.basename(root)
            # handle 'iso8583' inside 'codec' or 'io' etc, actually standard package name is usually the directory name
            # Let's read one of the go files to find the exact package name
            pkg = pkg_name
            for f in files:
                if f.endswith(".go") and not f.endswith("_test.go"):
                    with open(os.path.join(root, f), 'r') as go_file:
                        for line in go_file:
                            if line.startswith("package "):
                                pkg = line.split(" ")[1].strip()
                                break
                    break
            
            doc_file = os.path.join(root, "doc.go")
            if not os.path.exists(doc_file):
                content = f"""// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package {pkg} provides functionality for the fluxrig platform.
package {pkg}
"""
                with open(doc_file, 'w') as f:
                    f.write(content)

if __name__ == "__main__":
    main()
