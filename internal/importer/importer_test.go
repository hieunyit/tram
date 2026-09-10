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

// TestAnsibleProxyJumpVariable covers the spelling that names a jump station
// directly, which is clearer than hiding it in raw ssh arguments and so wins
// over them.
func TestAnsibleProxyJumpVariable(t *testing.T) {
	src := "[web]\n" +
		"a ansible_proxy_jump=bastion\n" +
		"b ansible_ssh_proxy_jump=other\n" +
		"c ansible_ssh_common_args='-J fromargs'\n" +
		"d ansible_proxy_jump=wins ansible_ssh_common_args='-J loses'\n"
	res := parse(t, "hosts", src, Options{})
	for name, want := range map[string]string{"a": "bastion", "b": "other", "c": "fromargs", "d": "wins"} {
		r, ok := byName(res, name)
		if !ok {
			t.Errorf("%s missing", name)
			continue
		}
		if r.ProxyJump != want {
			t.Errorf("%s jump = %q, want %q", name, r.ProxyJump, want)
		}
	}
}

// TestGroupNormalisation checks that a group written with stray spaces is the
// same group as one written without, rather than a second one that merely looks
// alike in a listing.
func TestGroupNormalisation(t *testing.T) {
	cases := map[string]string{
		" giavang / web-chat / ": "giavang/web-chat",
		"giavang//web":           "giavang/web",
		"  ":                     "",
		"prod":                   "prod",
	}
	for in, want := range cases {
		if got := NormaliseGroup(in); got != want {
			t.Errorf("NormaliseGroup(%q) = %q, want %q", in, got, want)
		}
	}

	res := parse(t, "s.csv", "name,ip,group\nweb1,1.1.1.1, prod / web \n", Options{})
	if r, _ := byName(res, "web1"); r.Group != "prod/web" {
		t.Errorf("group = %q", r.Group)
	}
	// A prefix joins cleanly rather than doubling the separator.
	res = parse(t, "s.csv", "name,ip,group\nweb1,1.1.1.1,/prod/\n", Options{GroupPrefix: "imported/"})
	if r, _ := byName(res, "web1"); r.Group != "imported/prod" {
		t.Errorf("prefixed group = %q", r.Group)
	}
}

// TestCSVAccountAndExtraHeadings covers the column names a spreadsheet from the
// older tool uses.
func TestCSVAccountAndExtraHeadings(t *testing.T) {
	src := "Name,Host Name,Folder,Account,SSH Key\n" +
		"web1,10.0.0.1,prod/web,deploy,~/.ssh/id_ed25519\n"
	res := parse(t, "s.csv", src, Options{})
	r, ok := byName(res, "web1")
	if !ok {
		t.Fatalf("web1 missing from %v", names(res))
	}
	if r.HostName != "10.0.0.1" {
		t.Errorf("hostname = %q; the host_name heading was not read", r.HostName)
	}
	if r.Group != "prod/web" {
		t.Errorf("group = %q; the folder heading was not read", r.Group)
	}
	if r.Account != "deploy" {
		t.Errorf("account = %q", r.Account)
	}
	if r.Key != "~/.ssh/id_ed25519" {
		t.Errorf("key = %q; the ssh_key heading was not read", r.Key)
	}
}

// TestIdentityHeadingStillMeansAKey guards a heading whose meaning is easy to
// get wrong: in the tool people are migrating from, "identity" is the key file,
// not an account.
func TestIdentityHeadingStillMeansAKey(t *testing.T) {
	res := parse(t, "s.csv", "name,ip,identity\nweb1,1.1.1.1,~/.ssh/id_rsa\n", Options{})
	r, _ := byName(res, "web1")
	if r.Key != "~/.ssh/id_rsa" {
		t.Errorf("key = %q", r.Key)
	}
	if r.Account != "" {
		t.Errorf("identity was read as an account: %q", r.Account)
	}
}

// TestGroupReplacesRatherThanNests is a bug report turned into a test.
//
// Asking for group "vpb-prod" put the hosts in "vpb-prod/vpb", because the
// option prefixed the file's own groups instead of replacing them. Naming a
// group is a plain instruction and should not invent a deeper one.
func TestGroupReplacesRatherThanNests(t *testing.T) {
	src := "[vpb]\nvpb-fs17 ansible_host=10.0.0.17\nvpb-fs18 ansible_host=10.0.0.18\n\n[other]\nx ansible_host=10.0.0.9\n"

	res := parse(t, "hosts", src, Options{Group: "vpb-prod"})
	for _, r := range res.Records {
		if r.Group != "vpb-prod" {
			t.Errorf("%s landed in %q, want vpb-prod exactly", r.Name, r.Group)
		}
	}

	// The nesting behaviour is still available, under a name that says so.
	res = parse(t, "hosts", src, Options{GroupPrefix: "vpb-prod"})
	if r, _ := byName(res, "vpb-fs17"); r.Group != "vpb-prod/vpb" {
		t.Errorf("with a prefix the group is %q, want vpb-prod/vpb", r.Group)
	}

	// Given both, the definite instruction wins.
	res = parse(t, "hosts", src, Options{Group: "chosen", GroupPrefix: "ignored"})
	if r, _ := byName(res, "vpb-fs17"); r.Group != "chosen" {
		t.Errorf("group = %q, the explicit group should win over the prefix", r.Group)
	}
}

// TestOverridesApplyToEveryHost covers the other half of the same report: an
// inventory names machines without saying how to log in to them, and editing
// that in afterwards one host at a time is not a workflow.
func TestOverridesApplyToEveryHost(t *testing.T) {
	src := "[vpb]\na ansible_host=10.0.0.1\nb ansible_host=10.0.0.2 ansible_user=fromfile\n"
	res := parse(t, "hosts", src, Options{
		Set: Overrides{User: "root", Key: "~/.ssh/id_ed25519", ProxyJump: "bastion", Account: "ops"},
	})
	if len(res.Records) != 2 {
		t.Fatalf("%d records", len(res.Records))
	}
	for _, r := range res.Records {
		if r.User != "root" {
			t.Errorf("%s user = %q; the override applies to every host, including one the file gave a user", r.Name, r.User)
		}
		if r.Key != "~/.ssh/id_ed25519" || r.ProxyJump != "bastion" || r.Account != "ops" {
			t.Errorf("%s = %+v", r.Name, r)
		}
	}

	// Nothing set means the file still decides.
	res = parse(t, "hosts", src, Options{})
	if r, _ := byName(res, "b"); r.User != "fromfile" {
		t.Errorf("without an override the file's user was lost: %q", r.User)
	}
	if r, _ := byName(res, "a"); r.User != "" {
		t.Errorf("a user appeared from nowhere: %q", r.User)
	}
}

func TestOverridesAny(t *testing.T) {
	if (Overrides{}).Any() {
		t.Error("empty overrides read as set")
	}
	if !(Overrides{User: "root"}).Any() {
		t.Error("a user override did not read as set")
	}
}
