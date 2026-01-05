package main

import (
	"database/sql"
	"fmt"

	_ "github.com/marcboeker/go-duckdb"
)

func main() {
	path := "data/fluxrig_test.duckdb"

	fmt.Printf("Opening DB: %s\n", path)
	db, err := sql.Open("duckdb", path)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	fmt.Println("Querying racks table...")
	// Handle error if table doesn't exist (e.g. if DB is empty)
	rows, err := db.Query("SELECT machine_id, name, status, ip, port, first_seen, last_seen, stats FROM racks")
	if err != nil {
		fmt.Printf("Query error (Table might not exist): %v\n", err)
		return
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
		var id int
		var name, status, ip string
		var port int
		var first, last any
		var stats any

		if err := rows.Scan(&id, &name, &status, &ip, &port, &first, &last, &stats); err != nil {
			fmt.Printf("Scan error: %v\n", err)
			continue
		}
		fmt.Printf("ROW: ID=%d Name=%s Status=%s IP=%s Port=%d\n", id, name, status, ip, port)
	}
	fmt.Printf("Total Rows: %d\n", count)
}
