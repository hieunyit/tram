package cmd

import "testing"

// TestParseFactsReadsRealOutput covers the four shapes uptime is written in.
//
// The command tram runs is deliberately plain, which means the parsing has to
// cope with what each system happens to print rather than with one agreed
// format. A field it cannot read is left empty and drawn as a dash; a field it
// reads wrongly would be a lie on screen, so the cases that differ are all here.
func TestParseFactsReadsRealOutput(t *testing.T) {
	cases := []struct {
		name                        string
		out                         string
		os, uptime, load, disk, ram string
	}{
		{
			name: "linux, with the disk and memory lines the command also asks for",
			out: "Linux 5.15.0-88-generic\n" +
				" 14:23:01 up 12 days,  3:44,  2 users,  load average: 0.08, 0.09, 0.10\n" +
				"/dev/sda1       41152736 12345678  26890000  32% /\n" +
				"Mem:          15927        8123        1204         512        6600        7300\n",
			os:     "Linux 5.15.0-88-generic",
			uptime: "12 days,  3:44",
			load:   "0.08",
			disk:   "32%",
			ram:    "51%",
		},
		{
			name:   "macos writes load averages without commas",
			out:    "Darwin 23.1.0\n14:23  up 12 days,  3:44, 2 users, load averages: 1.20 1.30 1.40\n",
			os:     "Darwin 23.1.0",
			uptime: "12 days,  3:44",
			load:   "1.20",
		},
		{
			name:   "busybox has no user count",
			out:    "Linux 4.14.98\n 14:23:01 up 3 min,  load average: 0.00, 0.01, 0.05\n",
			os:     "Linux 4.14.98",
			uptime: "3 min",
			load:   "0.00",
		},
		{
			name: "a device with neither command answers nothing",
			out:  "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			os, up, load, disk, ram := parseFacts(c.out)
			if os != c.os {
				t.Errorf("system = %q, want %q", os, c.os)
			}
			if up != c.uptime {
				t.Errorf("uptime = %q, want %q", up, c.uptime)
			}
			if load != c.load {
				t.Errorf("load = %q, want %q", load, c.load)
			}
			if disk != c.disk {
				t.Errorf("disk = %q, want %q", disk, c.disk)
			}
			if ram != c.ram {
				t.Errorf("memory = %q, want %q", ram, c.ram)
			}
		})
	}
}

// TestParseFactsIgnoresAnErrorMessage checks that a shell complaining about a
// missing command does not end up drawn as the operating system.
func TestParseFactsIgnoresAnErrorMessage(t *testing.T) {
	os, up, load, _, _ := parseFacts("Linux 6.1.0\n 09:00:01 up 1 day,  2:00,  1 user,  load average: 0.50, 0.40, 0.30\n")
	if os != "Linux 6.1.0" || load != "0.50" || up != "1 day,  2:00" {
		t.Fatalf("parsed %q / %q / %q", os, up, load)
	}
}
