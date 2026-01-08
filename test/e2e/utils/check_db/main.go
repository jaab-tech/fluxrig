// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/marcboeker/go-duckdb"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: check_db <path>")
		os.Exit(1)
	}
	path := os.Args[1]

	// Open in READ_ONLY mode to avoid locking if possible,
	// but DuckDB single-process write lock might block us if not WAL.
	// We try standard open.
	db, err := sql.Open("duckdb", path)
	if err != nil {
		fmt.Printf("Error opening DB: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.Query("SELECT machine_id, name, status FROM racks")
	if err != nil {
		fmt.Printf("Error querying: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = rows.Close() }()

	count := 0
	for rows.Next() {
		count++
		var id int
		var name, status string
		if err := rows.Scan(&id, &name, &status); err != nil {
			fmt.Printf("Error scanning: %v\n", err)
			continue
		}
		fmt.Printf("ROW: %d %s %s\n", id, name, status)
	}
	if err := rows.Err(); err != nil {
		fmt.Printf("Error iterating rows: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Found %d rows\n", count)
}
