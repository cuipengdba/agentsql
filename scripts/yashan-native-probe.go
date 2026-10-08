//go:build yashan_probe

package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	_ "github.com/yashan-technologies/yashandb-go"
)

var nativeCode = regexp.MustCompile(`YAS-[0-9]{5}`)

func main() {
	if stage := probe(); stage != "" {
		fmt.Fprintln(os.Stderr, "FAIL:", stage)
		os.Exit(1)
	}
}

// probe prints fixed outcomes and server error codes only. It never prints a
// credential, SQL statement, or raw driver error, including on cleanup errors.
func probe() (failure string) {
	password := os.Getenv("YASHAN_PASSWORD")
	if password == "" {
		return "YASHAN_PASSWORD is required"
	}
	host := os.Getenv("YASHAN_HOST")
	if host == "" {
		host = "host.docker.internal"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	sys, err := sql.Open("yasdb", nativeDSN("SYS", password, host))
	if err != nil {
		return "open SYS driver"
	}
	defer sys.Close()
	if err := sys.PingContext(ctx); err != nil {
		return "ping SYS"
	}
	fmt.Println("PASS: native driver Ping")
	var bound string
	if err := sys.QueryRowContext(ctx, "SELECT ? FROM DUAL", "bound-ok").Scan(&bound); err != nil || bound != "bound-ok" {
		return "bound SELECT"
	}
	fmt.Println("PASS: parameter binding")
	identifierBytes := make([]byte, 4)
	if _, err := rand.Read(identifierBytes); err != nil {
		return "generate temporary names"
	}
	suffix := strings.ToUpper(hex.EncodeToString(identifierBytes))
	table := "AGSQL_B71_T_" + suffix
	reader := "AGSQL_B71_R_" + suffix
	readerPasswordBytes := make([]byte, 12)
	if _, err := rand.Read(readerPasswordBytes); err != nil {
		return "generate temporary credential"
	}
	readerPassword := "Aa1" + hex.EncodeToString(readerPasswordBytes)
	tableCreated := false
	readerCreated := false
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if readerCreated {
			if _, err := sys.ExecContext(cleanupCtx, "DROP USER "+reader+" CASCADE"); err != nil {
				failure = "remove temporary reader"
			}
		}
		if tableCreated {
			if _, err := sys.ExecContext(cleanupCtx, "DROP TABLE "+table); err != nil {
				failure = "remove temporary table"
			}
		}
		if failure == "" {
			fmt.Println("PASS: temporary reader and table removed")
		}
	}()
	if _, err := sys.ExecContext(ctx, "CREATE TABLE "+table+" (ID NUMBER, PHONE VARCHAR2(32))"); err != nil {
		return "create temporary table"
	}
	tableCreated = true
	if _, err := sys.ExecContext(ctx, "INSERT INTO "+table+" (ID, PHONE) VALUES (1, '13800135678')"); err != nil {
		return "seed temporary table"
	}
	if _, err := sys.ExecContext(ctx, "CREATE USER "+reader+" IDENTIFIED BY "+readerPassword); err != nil {
		return "create temporary reader"
	}
	readerCreated = true
	if _, err := sys.ExecContext(ctx, "GRANT CREATE SESSION TO "+reader); err != nil {
		return "grant reader session"
	}
	if _, err := sys.ExecContext(ctx, "GRANT SELECT ON "+table+" TO "+reader); err != nil {
		return "grant table SELECT"
	}
	readDB, err := sql.Open("yasdb", nativeDSN(reader, readerPassword, host))
	if err != nil {
		return "open temporary reader"
	}
	defer readDB.Close()
	if err := readDB.PingContext(ctx); err != nil {
		return "ping temporary reader"
	}
	var phone string
	if err := readDB.QueryRowContext(ctx, "SELECT PHONE FROM SYS."+table+" WHERE ID = ?", 1).Scan(&phone); err != nil || phone != "13800135678" {
		return "bound reader SELECT"
	}
	fmt.Println("PASS: least-privilege reader bound SELECT")
	_, err = readDB.ExecContext(ctx, "INSERT INTO SYS."+table+" (ID, PHONE) VALUES (2, 'x')")
	if err == nil {
		return "reader write unexpectedly succeeded"
	}
	code := nativeCode.FindString(err.Error())
	if code == "" {
		return "reader write denied without YAS code"
	}
	fmt.Println("PASS: least-privilege write denied; code=" + code)
	return ""
}

func nativeDSN(username, password, host string) string {
	escape := strings.NewReplacer(`\`, `\\`, `/`, `\/`, `@`, `\@`)
	return escape.Replace(username) + "/" + escape.Replace(password) + "@" + host + ":1688?compat_vector=yashan"
}
