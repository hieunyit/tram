package importer

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"sort"
	"strings"
)

// csvAliases maps the column headings people actually write onto tram's
// fields. The list is generous on purpose: an inventory exported from a
// spreadsheet is not going to use tram's vocabulary, and rejecting a file
// because a column is called "ip" rather than "hostname" helps nobody.
var csvAliases = map[string]string{
	"name": "name", "host": "name", "alias": "name", "label": "name", "id": "name",
	"hostname": "hostname", "address": "hostname", "addr": "hostname", "ip": "hostname",
	"ip_address": "hostname", "ipaddress": "hostname", "fqdn": "hostname",
	"server": "hostname", "host_name": "hostname",
	"user": "user", "username": "user", "login": "user", "ssh_user": "user",
	"port": "port", "ssh_port": "port",
	"key": "key", "keyfile": "key", "key_file": "key", "ssh_key": "key", "identity": "key",
	"identityfile": "key", "identity_file": "key", "private_key": "key", "pem": "key",
	"jump": "jump", "proxyjump": "jump", "proxy_jump": "jump", "bastion": "jump", "via": "jump",
	"group": "group", "groups": "group", "environment": "group", "env": "group",
	"tag": "group", "folder": "group",
	"account": "account", "acc": "account",
	"desc": "desc", "description": "desc", "comment": "desc", "note": "desc", "notes": "desc",
}

// parseCSV reads a spreadsheet export. A header row is required, because
// guessing which column is a user name and which is a host name from the values
// alone is exactly the kind of guess that produces a plausible, wrong config.
func parseCSV(data []byte, opt Options) (*Result, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	if bytes.Count(firstLine(data), []byte{'\t'}) > bytes.Count(firstLine(data), []byte{','}) {
		r.Comma = '\t'
	}

	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse CSV: %w", err)
	}
	if len(rows) == 0 {
		return &Result{}, nil
	}

	header := rows[0]
	cols := make([]string, len(header))
	haveName := false
	for i, h := range header {
		// A spreadsheet export often starts with a byte order mark, which would
		// otherwise make the first heading unrecognisable.
		key := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, bom)))
		key = strings.ReplaceAll(key, " ", "_")
		field, ok := csvAliases[key]
		if !ok {
			continue
		}
		cols[i] = field
		if field == "name" {
			haveName = true
		}
	}
	if !haveName {
		return nil, fmt.Errorf(
			"no column names the host; the first row must have a heading such as %s",
			strings.Join(headingsFor("name"), ", "))
	}

	res := &Result{}
	for n, row := range rows[1:] {
		rec := Record{Where: fmt.Sprintf("row %d", n+2)}
		for i, cell := range row {
			if i >= len(cols) || cols[i] == "" {
				continue
			}
			cell = strings.TrimSpace(cell)
			switch cols[i] {
			case "name":
				rec.Name = cell
			case "hostname":
				rec.HostName = cell
			case "user":
				rec.User = cell
			case "port":
				rec.Port = cell
			case "key":
				rec.Key = cell
			case "jump":
				rec.ProxyJump = cell
			case "group":
				rec.Group = NormaliseGroup(strings.ReplaceAll(cell, "\\", "/"))
			case "account":
				rec.Account = cell
			case "desc":
				rec.Desc = cell
			}
		}
		if rec.Name == "" {
			continue // a blank row, not an error
		}
		if !validName(rec.Name) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %q cannot be an ssh_config host name", rec.Where, rec.Name))
			continue
		}
		if rec.Group != "" {
			rec.Groups = []string{rec.Group}
		}
		res.Records = append(res.Records, rec)
	}
	return res, nil
}

// bom is the byte order mark a spreadsheet export writes before the first
// heading, which would otherwise make that heading unrecognisable. It is built
// from its code point rather than written literally, because Go rejects a real
// byte order mark anywhere but at the very start of a source file.
var bom = string(rune(0xFEFF))

func firstLine(data []byte) []byte {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return data[:i]
	}
	return data
}

// headingsFor lists the accepted spellings of one field, for an error message
// that tells the user what to write instead.
func headingsFor(field string) []string {
	var out []string
	for alias, f := range csvAliases {
		if f == field {
			out = append(out, alias)
		}
	}
	sort.Strings(out)
	return out
}
