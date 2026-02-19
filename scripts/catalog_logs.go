// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	fmt.Println("level| message| file| line_number")
	fset := token.NewFileSet()

	dirs := []string{"pkg", "cmd"}
	for _, dir := range dirs {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}

			node, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return nil
			}

			ast.Inspect(node, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}

				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}

				x, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}

				if x.Name == "logger" || x.Name == "slog" || x.Name == "log" {
					method := sel.Sel.Name
					if method == "Info" || method == "Warn" || method == "Error" || method == "Debug" {
						if len(call.Args) > 0 {
							if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
								msg := strings.Trim(lit.Value, "\"")
								pos := fset.Position(call.Pos())
								fmt.Printf("%s| %s| %s| %d\n", method, msg, path, pos.Line)
							}
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error walking %s: %v\n", dir, err)
		}
	}
}
