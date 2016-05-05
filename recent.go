//Command recent(1) lists recently modified files.
//
//Given no arguments, it prints all non-dot files in the current directory
//that have been modified in the last 24 hours, appending a / to directory names.
//
//Given arguments, it prints files that are recent.
//If an argument is a directory, recent(1) behaves as if called in that directory.
//
//The default is thus
//	recent -d 1 .
//
//Everything recent(1) does can be done with find(1),
//but the find(1) invocations are elaborate and recent(1)
//is better suited for quickly, if less precisely, inspecting a directory.
//For example the default for recent(1) is equivalent to
//	$ find . -maxdepth 1 -mtime 0 -not -name ".*"
//
//
//There are two groups of flags to recent(1).
//Those that modify the time for which files are considered recent,
//and those that modify the behavior of recent(1) and its output.
//
//Time flags
//
//Time can be specified in
//	-min    minutes (always  60 seconds)
//	-h	    hours   (always  60 minutes)
//	-d		days    (always  24 hours)
//	-m      months  (always  30 days)
//  -y      years   (always 365 days)
//
//If multiple time flags are present, they are added together, so
//	recent -m 1 -d 3 -h 5
//prints files modified in the last month, three days, and five hours.
//
//A day and a half can be represented by either
//	recent -d 1 -h 12
//or by
//	recent -h 36
//and the previous example of one month, three days and five hours as
//	recent -d 33 -h 5
//or as
//	recent -h 797
//if clarity is unimportant.
//
//There is an implementation unit that the sum of the times
//must be less than 290 years.
//
//Modifier flags
//
//To avoid appending a / to directory names, use the -no/ flag.
//
//To include dot files, use the -. flag.
//Dot files provided as an explicit command line argument
//are included in the search regardless.
//
//To show files that haven't been modified recently, use the -v flag.
//
//For scripting, the -q flag suppresses printing matches.
//If there are matches, it exits with 0.
//If there are no matches, it exits with 1.
//Errors are still logged to stderr.
//
//The -print0 flag is the same as on find(1),
//it separates files with null rather than a newline,
//for use with xargs(1).
//
//Examples
//
//All examples assume the following files in the current directory:
//	.a   # modified 14 hours ago
//	b    # modified 15 days ago
//	c/   # modified 5 seconds ago
//	c/d  # modified 2 hours ago
//	c/e  # modified over a year ago
//	c/.f # modified 4 days ago
//
//	$ recent
//	c/
//The only recently modified file is the directory c.
//
//	$ recent -v
//	b
//The only file not modified recently is b.
//
//	$ recent c
//	c/d
//The only recently modified file in the directory c is d.
//Note that c/ itself is not printed.
//
//	$ recent -no/ -.
//	.a
//	c
//If we include dot files .a is also a recently modified file.
//
//	$ recent .a
//	.a
//We do not need to specify -. if asking about an explicitly named file.
//
//	$ recent *
//	c/d
//Since the shell expands * to b c/, this is equivalent to
//	$ recent b c
//and given a directory name as a command line argument recent(1)
//searches the entries of that directory, in this case matching c/d.
//
//	$ recent -. -v -d 2 *
//	b
//	c/e
//	c/.f
//This invocation looks for all files that haven't been modified in the last two days.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"
)

//helper to sort and display flags

type usage struct {
	name, value string
	first       bool
}

type usages []usage

func (u usages) Len() int {
	return len(u)
}

func (u usages) Swap(i, j int) {
	u[j], u[i] = u[i], u[j]
}

func (u usages) Less(i, j int) bool {
	if u[i].first != u[j].first {
		return u[i].first
	}
	return u[i].name < u[j].name
}

func init() {
	log.SetFlags(0)

	flag.Usage = func() {
		log.Printf("Usage: %s [flags] [files]\n", os.Args[0])
		var us usages
		flag.VisitAll(func(f *flag.Flag) {
			//adapted from stdlib
			s := fmt.Sprintf("  -%s", f.Name)
			name, usageString := flag.UnquoteUsage(f)
			if len(name) > 0 {
				s += " " + name
			}

			//right now we either have bool or uint flags: might get more complicated
			//with other kinds of flags.
			_, bf := f.Value.(interface {
				IsBoolFlag() bool
			})

			if bf {
				for len(s) < 10 { //pad to the longest boolean flag defined so far
					s += " "
				}
				s += usageString //don't want this on non-boolean flags
			}

			us = append(us, usage{
				name:  f.Name,
				value: s,
				first: !bf,
			})
		})
		sort.Sort(us)
		for _, u := range us {
			log.Print(u.value)
		}
	}
}

func overflowCheck(years, months, days, hours, minutes *uint) {
	//convert everything into years and make sure
	//that it doesn't exceed a rough upper bound
	//on the number of representable years
	y := *years
	M := *months / 12
	d := *days / 365
	h := *hours / (24 * 365)
	m := *minutes / (60 * 24 * 365)
	if y+M+d+h+m > 290 {
		log.Fatal("Maximum duration is 290 years. How old are your files?")
	}
}

func allZero(xs ...*uint) bool {
	for _, x := range xs {
		if *x != 0 {
			return false
		}
	}
	return true
}

func toDuration(years, months, days, hours, mins *uint) time.Duration {
	const (
		day   = 24 * time.Hour
		month = 30 * day
		year  = 365 * day
	)

	d := year * time.Duration(*years)
	d += month * time.Duration(*months)
	d += day * time.Duration(*days)
	d += time.Hour * time.Duration(*hours)
	d += time.Minute * time.Duration(*mins)
	return d
}

//Matcher handles all the various matching scenarios.
//Every nonbool needs to be set.
type Matcher struct {
	Invert      bool
	IncludeDots bool
	NoSlash     bool

	Recent time.Duration
	Now    time.Time

	Out func(string)
	Log func(error)
}

func (m *Matcher) dostat(skipdotcheck bool, prefix, name string, fi os.FileInfo) {
	if !skipdotcheck && !m.IncludeDots && filepath.Base(name)[0] == '.' {
		return
	}

	hit := m.Now.Sub(fi.ModTime()) < m.Recent
	if m.Invert {
		hit = !hit
	}

	if hit {
		name = filepath.Clean(filepath.Join(prefix, name))
		if fi.IsDir() && !m.NoSlash {
			name += "/"
		}
		m.Out(name)
	}
}

//Match a list of names from command line arguments.
func (m *Matcher) Match(files []string) {
	for _, file := range files {
		fi, err := os.Stat(file)
		if err != nil {
			m.Log(err)
			continue
		}
		//if we're explicitly given a directory, assume we are to read it.
		if fi.IsDir() {
			m.Readdir(file)
		} else {
			m.dostat(true, "", file, fi)
		}
	}
}

//Readdir matches the content of a directory
func (m *Matcher) Readdir(dname string) {
	d, err := os.Open(dname)
	if err != nil {
		m.Log(err)
		return
	}

	for {
		fis, err := d.Readdir(100)
		for _, fi := range fis {
			m.dostat(false, dname, fi.Name(), fi)
		}
		if err != nil {
			if err != io.EOF {
				m.Log(err)
			}
			return
		}
	}
}

func main() {
	//Initial setup

	matcher := Matcher{Now: time.Now()}

	flag.BoolVar(&matcher.Invert, "v", false, "invert matches")
	flag.BoolVar(&matcher.IncludeDots, ".", false, "include dot files")
	flag.BoolVar(&matcher.NoSlash, "no/", false, "do not print / after directory names")

	var (
		noPrint = flag.Bool("q", false, "print nothing, exit with 1 if no files are recent")
		print0  = flag.Bool("print0", false, "print files separated by null instead of newline")

		mins   = flag.Uint("min", 0, "`minutes`")
		hours  = flag.Uint("h", 0, "`hours`")
		days   = flag.Uint("d", 0, "`days`")
		months = flag.Uint("m", 0, "`months`")
		years  = flag.Uint("y", 0, "`years`")
	)

	flag.Parse()

	//validate additional constraints
	overflowCheck(years, months, days, hours, mins)
	if *print0 && *noPrint {
		log.Fatal("-print0 and -q are fundamentally opposed ideas.")
	}

	//default to one day
	if allZero(years, months, days, hours, mins) {
		*days = 1
	}

	//Configure

	matcher.Recent = toDuration(years, months, days, hours, mins)

	matcher.Log = func(e error) {
		log.Println(e)
	}

	matched := false //only used if *noPrint
	if *noPrint {
		matcher.Out = func(string) {
			matched = true
		}
	} else if *print0 {
		matcher.Out = func(s string) {
			fmt.Printf("%s\000", s)
		}
	} else {
		matcher.Out = func(s string) {
			fmt.Println(s)
		}
	}

	//Run

	if flag.NArg() > 0 {
		matcher.Match(flag.Args())
	} else {
		matcher.Readdir(".")
	}

	//if there were no matches and we were told not to print, use exit code
	if *noPrint && !matched {
		os.Exit(1)
	}
}
