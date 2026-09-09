package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func parse(t *testing.T, name, content string, opt Options) *Result {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Parse(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func byName(res *Result, name string) (Record, bool) {
	for _, r := range res.Records {
		if r.Name == name {
			return r, true
		}
	}
	return Record{}, false
}

func names(res *Result) []string {
	out := make([]string, len(res.Records))
	for i, r := range res.Records {
		out[i] = r.Name
	}
	return out
}

const iniInventory = `# a production inventory
[webservers]
web1 ansible_host=10.0.0.1 ansible_port=2222
web2 ansible_host=10.0.0.2

[dbservers]
db1 ansible_host=10.0.1.1 ansible_user=postgres

[local]
buildbox ansible_connection=local

[webservers:vars]
ansible_user=deploy
ansible_ssh_private_key_file=~/.ssh/deploy_ed25519

[prod:children]
webservers
dbservers

[prod:vars]
ansible_ssh_common_args=-o ProxyJump=bastion.example.com
`

// TestAnsibleINIMapsTheConnectionVariables checks that the variables which say
// how to reach a machine become ssh_config fields, and that the rest of an
// inventory is ignored.
func TestAnsibleINIMapsTheConnectionVariables(t *testing.T) {
	res := parse(t, "hosts", iniInventory, Options{})
	if res.Format != AnsibleINI {
		t.Fatalf("detected %s, want %s", res.Format, AnsibleINI)
	}

	web1, ok := byName(res, "web1")
	if !ok {
		t.Fatalf("web1 missing from %v", names(res))
	}
	if web1.HostName != "10.0.0.1" {
		t.Errorf("hostname = %q", web1.HostName)
	}
	if web1.Port != "2222" {
		t.Errorf("port = %q", web1.Port)
	}
	if web1.User != "deploy" {
		t.Errorf("user = %q, group vars should supply it", web1.User)
	}
	if web1.Key != "~/.ssh/deploy_ed25519" {
		t.Errorf("key = %q", web1.Key)
	}
	if web1.ProxyJump != "bastion.example.com" {
		t.Errorf("jump = %q, it comes from the parent group's ssh args", web1.ProxyJump)
	}

	db1, _ := byName(res, "db1")
	if db1.User != "postgres" {
		t.Errorf("db1 user = %q, its own line should beat the group", db1.User)
	}
	if db1.Key != "" {
		t.Errorf("db1 picked up the webservers key: %q", db1.Key)
	}
}

// TestAnsibleINIGroupNesting checks that children sections become tram's
// slash-separated group path.
func TestAnsibleINIGroupNesting(t *testing.T) {
	res := parse(t, "hosts", iniInventory, Options{})
	web1, _ := byName(res, "web1")
	if web1.Group != "prod/webservers" {
		t.Errorf("group = %q, want prod/webservers", web1.Group)
	}
	db1, _ := byName(res, "db1")
	if db1.Group != "prod/dbservers" {
		t.Errorf("group = %q, want prod/dbservers", db1.Group)
	}

	flat := parse(t, "hosts", iniInventory, Options{FlatGroups: true})
	if h, _ := byName(flat, "web1"); h.Group != "webservers" {
		t.Errorf("with --flat-groups the group is %q, want webservers", h.Group)
	}

	prefixed := parse(t, "hosts", iniInventory, Options{GroupPrefix: "imported"})
	if h, _ := byName(prefixed, "web1"); h.Group != "imported/prod/webservers" {
		t.Errorf("with a prefix the group is %q", h.Group)
	}
}

// TestNonSSHHostsAreSkipped covers a host Ansible reaches some other way. ssh
// cannot be pointed at it, so importing it would create a stanza that can only
// fail.
func TestNonSSHHostsAreSkipped(t *testing.T) {
	res := parse(t, "hosts", iniInventory, Options{})
	if _, ok := byName(res, "buildbox"); ok {
		t.Error("a host with ansible_connection=local was imported")
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "buildbox") {
		t.Errorf("the skip was not reported: %v", res.Skipped)
	}
}

// TestHostRanges covers Ansible's bracket ranges, including the zero padding
// that is the reason to write 01 rather than 1.
func TestHostRanges(t *testing.T) {
	res := parse(t, "hosts", "[web]\nweb[01:04] ansible_user=deploy\nnode-[a:c].example.com\n", Options{})
	want := []string{"web01", "web02", "web03", "web04", "node-a.example.com", "node-b.example.com", "node-c.example.com"}
	got := names(res)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ranges expanded to %v, want %v", got, want)
	}
	if h, _ := byName(res, "web03"); h.User != "deploy" {
		t.Errorf("an expanded host lost its variables: %+v", h)
	}
}

func TestHostRangeWithStep(t *testing.T) {
	res := parse(t, "hosts", "[web]\nweb[0:6:2]\n", Options{})
	if got := names(res); strings.Join(got, ",") != "web0,web2,web4,web6" {
		t.Errorf("stepped range gave %v", got)
	}
}

// TestHostInSeveralGroups checks that one machine listed twice stays one host,
// with its variables merged and the deeper group kept.
func TestHostInSeveralGroups(t *testing.T) {
	src := "[web]\nweb1 ansible_host=10.0.0.1\n\n[canary]\nweb1 ansible_port=2222\n\n[prod:children]\nweb\n"
	res := parse(t, "hosts", src, Options{})
	if len(res.Records) != 1 {
		t.Fatalf("%d records, want 1: %v", len(res.Records), names(res))
	}
	r := res.Records[0]
	if r.HostName != "10.0.0.1" || r.Port != "2222" {
		t.Errorf("variables were not merged across groups: %+v", r)
	}
	if r.Group != "prod/web" {
		t.Errorf("group = %q, the deeper path should win", r.Group)
	}
	if len(res.Warnings) == 0 {
		t.Error("dropping a group was not reported")
	}
}

const yamlInventory = `all:
  vars:
    ansible_user: root
  children:
    prod:
      children:
        webservers:
          hosts:
            web1:
              ansible_host: 10.0.0.1
              ansible_port: 2222
            web2:
              ansible_host: 10.0.0.2
          vars:
            ansible_user: deploy
        dbservers:
          hosts:
            db1: {ansible_host: 10.0.1.1}
    staging:
      hosts:
        stage1:
          ansible_host: 10.9.0.1
          ansible_ssh_common_args: -J bastion
`

func TestAnsibleYAML(t *testing.T) {
	res := parse(t, "inventory.yml", yamlInventory, Options{})
	if res.Format != AnsibleYAML {
		t.Fatalf("detected %s", res.Format)
	}
	if len(res.Records) != 4 {
		t.Fatalf("%d records, want 4: %v", len(res.Records), names(res))
	}

	web1, _ := byName(res, "web1")
	if web1.HostName != "10.0.0.1" || web1.Port != "2222" {
		t.Errorf("web1 = %+v", web1)
	}
	if web1.User != "deploy" {
		t.Errorf("web1 user = %q; the nearest group's vars should win over all's", web1.User)
	}
	if web1.Group != "prod/webservers" {
		t.Errorf("web1 group = %q", web1.Group)
	}

	db1, _ := byName(res, "db1")
	if db1.User != "root" {
		t.Errorf("db1 user = %q; it should inherit from all", db1.User)
	}

	stage1, _ := byName(res, "stage1")
	if stage1.ProxyJump != "bastion" {
		t.Errorf("a -J jump host was not read: %+v", stage1)
	}
	if stage1.Group != "staging" {
		t.Errorf("stage1 group = %q", stage1.Group)
	}
}

// TestYAMLPortStaysAnInteger guards against a number coming back as 2222.000000
// once it has been through a generic YAML decode.
func TestYAMLPortStaysAnInteger(t *testing.T) {
	res := parse(t, "i.yml", "all:\n  hosts:\n    a:\n      ansible_port: 2222\n", Options{})
	if r, _ := byName(res, "a"); r.Port != "2222" {
		t.Errorf("port = %q", r.Port)
	}
}

// TestYAMLHostsAsAList covers the shorter form where hosts are a list of names
// rather than a mapping.
func TestYAMLHostsAsAList(t *testing.T) {
	res := parse(t, "i.yml", "webservers:\n  hosts:\n    - web1\n    - web2\n", Options{})
	if len(res.Records) != 2 {
		t.Fatalf("%d records: %v", len(res.Records), names(res))
	}
	if r, _ := byName(res, "web1"); r.Group != "webservers" {
		t.Errorf("group = %q", r.Group)
	}
}

func TestCSVWithFriendlyHeadings(t *testing.T) {
	src := "Name,IP Address,User,Port,Group,Description\n" +
		"web1,10.0.0.1,deploy,2222,prod/web,Primary web server\n" +
		"db1,10.0.1.1,postgres,,prod/db,\n" +
		"\n"
	res := parse(t, "servers.csv", src, Options{})
	if res.Format != CSV {
		t.Fatalf("detected %s", res.Format)
	}
	if len(res.Records) != 2 {
		t.Fatalf("%d records: %v", len(res.Records), names(res))
	}
	web1, _ := byName(res, "web1")
	if web1.HostName != "10.0.0.1" || web1.User != "deploy" || web1.Port != "2222" {
		t.Errorf("web1 = %+v", web1)
	}
	if web1.Group != "prod/web" || web1.Desc != "Primary web server" {
		t.Errorf("web1 group/desc = %q / %q", web1.Group, web1.Desc)
	}
}

func TestCSVWithoutANameColumnIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.csv")
	if err := os.WriteFile(path, []byte("colour,size\nred,large\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Parse(path, Options{})
	if err == nil {
		t.Fatal("a CSV with no host column was accepted")
	}
	if !strings.Contains(err.Error(), "heading") {
		t.Errorf("the error does not say what to fix: %v", err)
	}
}

// TestDetectFromContent covers the common case of an Ansible inventory called
// `hosts`, with no extension to go on.
func TestDetectFromContent(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    Format
	}{
		{"hosts", "[web]\nweb1\n", AnsibleINI},
		{"hosts", "web1 ansible_host=1.1.1.1\n", AnsibleINI},
		{"inventory", "---\nall:\n  hosts:\n    a:\n", AnsibleYAML},
		{"inventory", "all:\n", AnsibleYAML},
		{"export", "name,ip\nweb1,1.1.1.1\n", CSV},
		{"hosts", "# only a comment\n[web]\n", AnsibleINI},
	}
	for _, c := range cases {
		if got := Detect(c.name, []byte(c.content)); got != c.want {
			t.Errorf("Detect(%q) = %s, want %s", c.content, got, c.want)
		}
	}
}

// TestProxyCommandIsNotTranslated covers a deliberate refusal: a command is not
// a host, and turning one into a ProxyJump would produce a stanza that looks
// right and connects somewhere else.
func TestProxyCommandIsNotTranslated(t *testing.T) {
	src := "[web]\nweb1 ansible_ssh_common_args='-o ProxyCommand=\"ssh -W %h:%p gw\"'\n"
	res := parse(t, "hosts", src, Options{})
	if r, _ := byName(res, "web1"); r.ProxyJump != "" {
		t.Errorf("a ProxyCommand was turned into ProxyJump %q", r.ProxyJump)
	}
}

func TestBadHostNamesAreRejectedNotEscaped(t *testing.T) {
	res := parse(t, "hosts", "[web]\nweb*glob\ngood1 ansible_host=1.1.1.1\n", Options{})
	if _, ok := byName(res, "web*glob"); ok {
		t.Error("a wildcard name was imported; ssh would read it as a pattern")
	}
	if _, ok := byName(res, "good1"); !ok {
		t.Error("one bad name stopped the rest of the file being read")
	}
	if len(res.Warnings) == 0 {
		t.Error("the rejection was not reported")
	}
}
