// Copyright 2016 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License.

// +build lint

package build_test

import (
	"bufio"
	"bytes"
	"go/build"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ghemawat/stream"
	"github.com/pkg/errors"
	"golang.org/x/tools/go/buildutil"
)

const cockroachDB = "github.com/cockroachdb/cockroach/pkg"

func dirCmd(
	dir string, name string, args ...string,
) (*exec.Cmd, *bytes.Buffer, stream.Filter, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stderr := new(bytes.Buffer)
	cmd.Stderr = stderr
	return cmd, stderr, stream.ReadLines(stdout), nil
}

func TestStyle(t *testing.T) {
	pkg, err := build.Import(cockroachDB, "", build.FindOnly)
	if err != nil {
		t.Skip(err)
	}

	t.Run("TestCopyrightHeaders", func(t *testing.T) {
		t.Parallel()
		cmd, stderr, filter, err := dirCmd(pkg.Dir, "git", "grep", "-LE", `^// (Copyright|Code generated by)`, "--", "*.go")
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		if err := stream.ForEach(filter, func(s string) {
			t.Errorf(`%s <- missing license header`, s)
		}); err != nil {
			t.Error(err)
		}

		if err := cmd.Wait(); err != nil {
			if out := stderr.String(); len(out) > 0 {
				t.Fatalf("err=%s, stderr=%s", err, out)
			}
		}
	})

	t.Run("TestMissingLeakTest", func(t *testing.T) {
		t.Parallel()
		cmd, stderr, filter, err := dirCmd(pkg.Dir, "util/leaktest/check-leaktest.sh")
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		if err := stream.ForEach(filter, func(s string) {
			t.Error(s)
		}); err != nil {
			t.Error(err)
		}

		if err := cmd.Wait(); err != nil {
			if out := stderr.String(); len(out) > 0 {
				t.Fatalf("err=%s, stderr=%s", err, out)
			}
		}
	})

	t.Run("TestTabsInShellScripts", func(t *testing.T) {
		t.Parallel()
		cmd, stderr, filter, err := dirCmd(pkg.Dir, "git", "grep", "-nF", "\t", "--", "*.sh")
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		if err := stream.ForEach(filter, func(s string) {
			t.Errorf(`%s <- tab detected, use spaces instead`, s)
		}); err != nil {
			t.Error(err)
		}

		if err := cmd.Wait(); err != nil {
			if out := stderr.String(); len(out) > 0 {
				t.Fatalf("err=%s, stderr=%s", err, out)
			}
		}
	})

	t.Run("TestEnvutil", func(t *testing.T) {
		t.Parallel()
		cmd, stderr, filter, err := dirCmd(pkg.Dir, "git", "grep", "-nE", `os\.(Getenv|LookupEnv)`, "--", "*.go")
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		if err := stream.ForEach(stream.Sequence(
			filter,
			stream.GrepNot(`^cmd(/.*)?/\w+\.go\b`),
			stream.GrepNot(`^build/style_test\.go\b`),
			stream.GrepNot(`^ccl/(sqlccl/backup_cloud|storageccl/export_storage|acceptanceccl/backup)_test\.go\b`),
			stream.GrepNot(`^acceptance(/.*)?/\w+\.go\b`),
			stream.GrepNot(`^util/(log|envutil|sdnotify)/\w+\.go\b`),
		), func(s string) {
			t.Errorf(`%s <- forbidden; use "envutil" instead`, s)
		}); err != nil {
			t.Error(err)
		}

		if err := cmd.Wait(); err != nil {
			if out := stderr.String(); len(out) > 0 {
				t.Fatalf("err=%s, stderr=%s", err, out)
			}
		}
	})

	t.Run("TestSyncutil", func(t *testing.T) {
		t.Parallel()
		cmd, stderr, filter, err := dirCmd(pkg.Dir, "git", "grep", "-nE", `sync\.(RW)?Mutex`, "--", "*.go")
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		if err := stream.ForEach(stream.Sequence(
			filter,
			stream.GrepNot(`^util/syncutil/mutex_sync\.go\b`),
		), func(s string) {
			t.Errorf(`%s <- forbidden; use "syncutil.{,RW}Mutex" instead`, s)
		}); err != nil {
			t.Error(err)
		}

		if err := cmd.Wait(); err != nil {
			if out := stderr.String(); len(out) > 0 {
				t.Fatalf("err=%s, stderr=%s", err, out)
			}
		}
	})

	t.Run("TestTodoStyle", func(t *testing.T) {
		t.Parallel()
		cmd, stderr, filter, err := dirCmd(pkg.Dir, "git", "grep", "-nE", `\sTODO\([^)]*\)[^:]`, "--", "*.go")
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		if err := stream.ForEach(filter, func(s string) {
			t.Errorf(`%s <- use 'TODO(...): ' instead`, s)
		}); err != nil {
			t.Error(err)
		}

		if err := cmd.Wait(); err != nil {
			if out := stderr.String(); len(out) > 0 {
				t.Fatalf("err=%s, stderr=%s", err, out)
			}
		}
	})

	t.Run("TestTimeutil", func(t *testing.T) {
		t.Parallel()
		cmd, stderr, filter, err := dirCmd(pkg.Dir, "git", "grep", "-nE", `time\.(Now|Since)`, "--", "*.go")
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		if err := stream.ForEach(stream.Sequence(
			filter,
			stream.GrepNot(`^util/(log|syncutil|timeutil|tracing)/\w+\.go\b`),
		), func(s string) {
			t.Errorf(`%s <- forbidden; use "timeutil" instead`, s)
		}); err != nil {
			t.Error(err)
		}

		if err := cmd.Wait(); err != nil {
			if out := stderr.String(); len(out) > 0 {
				t.Fatalf("err=%s, stderr=%s", err, out)
			}
		}
	})

	t.Run("TestGrpc", func(t *testing.T) {
		t.Parallel()
		cmd, stderr, filter, err := dirCmd(pkg.Dir, "git", "grep", "-nE", `grpc.NewServer\([^)]*\)`, "--", "*.go")
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		if err := stream.ForEach(stream.Sequence(
			filter,
			stream.GrepNot(`^rpc/context(_test)?\.go\b`),
		), func(s string) {
			t.Errorf(`%s <- forbidden; use "rpc.NewServer" instead`, s)
		}); err != nil {
			t.Error(err)
		}

		if err := cmd.Wait(); err != nil {
			if out := stderr.String(); len(out) > 0 {
				t.Fatalf("err=%s, stderr=%s", err, out)
			}
		}
	})

	t.Run("TestProtoClone", func(t *testing.T) {
		t.Parallel()
		cmd, stderr, filter, err := dirCmd(pkg.Dir, "git", "grep", "-nE", `\.Clone\([^)]+\)`, "--", "*.go")
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		if err := stream.ForEach(stream.Sequence(
			filter,
			stream.GrepNot(`protoutil\.Clone\([^)]+\)`),
			stream.GrepNot(`^util/protoutil/clone(_test)?\.go\b`),
		), func(s string) {
			t.Errorf(`%s <- forbidden; use "protoutil.Clone" instead`, s)
		}); err != nil {
			t.Error(err)
		}

		if err := cmd.Wait(); err != nil {
			if out := stderr.String(); len(out) > 0 {
				t.Fatalf("err=%s, stderr=%s", err, out)
			}
		}
	})

	t.Run("TestProtoMarshal", func(t *testing.T) {
		t.Parallel()
		cmd, stderr, filter, err := dirCmd(pkg.Dir, "git", "grep", "-nE", `\.Marshal\([^)]+\)`, "--", "*.go")
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		if err := stream.ForEach(stream.Sequence(
			filter,
			stream.GrepNot(`(json|yaml|protoutil|Field)\.Marshal`),
			stream.GrepNot(`^util/protoutil/marshal(_test)?\.go\b`),
		), func(s string) {
			t.Errorf(`%s <- forbidden; use "protoutil.Marshal" instead`, s)
		}); err != nil {
			t.Error(err)
		}

		if err := cmd.Wait(); err != nil {
			if out := stderr.String(); len(out) > 0 {
				t.Fatalf("err=%s, stderr=%s", err, out)
			}
		}
	})

	t.Run("TestImportNames", func(t *testing.T) {
		t.Parallel()
		cmd, stderr, filter, err := dirCmd(pkg.Dir, "git", "grep", "-nE", `^(import|\s+)(\w+ )?"database/sql"$`, "--", "*.go")
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		if err := stream.ForEach(stream.Sequence(
			filter,
			stream.GrepNot(`gosql "database/sql"`),
		), func(s string) {
			t.Errorf(`%s <- forbidden; import "database/sql" as "gosql" to avoid confusion with "cockroach/sql"`, s)
		}); err != nil {
			t.Error(err)
		}

		if err := cmd.Wait(); err != nil {
			if out := stderr.String(); len(out) > 0 {
				t.Fatalf("err=%s, stderr=%s", err, out)
			}
		}
	})

	t.Run("TestMisspell", func(t *testing.T) {
		t.Parallel()
		cmd, stderr, filter, err := dirCmd(pkg.Dir, "git", "ls-files")
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		if err := stream.ForEach(stream.Sequence(
			filter,
			stream.Map(func(s string) string {
				return filepath.Join(pkg.Dir, s)
			}),
			stream.Xargs("misspell"),
		), func(s string) {
			t.Errorf(s)
		}); err != nil {
			t.Error(err)
		}

		if err := cmd.Wait(); err != nil {
			if out := stderr.String(); len(out) > 0 {
				t.Fatalf("err=%s, stderr=%s", err, out)
			}
		}
	})

	t.Run("TestGofmtSimplify", func(t *testing.T) {
		t.Parallel()
		cmd, stderr, filter, err := dirCmd(pkg.Dir, "gofmt", "-s", "-d", "-l", ".")
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		if err := stream.ForEach(filter, func(s string) {
			t.Error(s)
		}); err != nil {
			t.Error(err)
		}

		if err := cmd.Wait(); err != nil {
			if out := stderr.String(); len(out) > 0 {
				t.Fatalf("err=%s, stderr=%s", err, out)
			}
		}
	})

	t.Run("TestCrlfmt", func(t *testing.T) {
		t.Parallel()
		cmd, stderr, filter, err := dirCmd(pkg.Dir, "crlfmt", "-ignore", `\.pb(\.gw)?\.go`, "-tab", "2", ".")
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		if err := stream.ForEach(filter, func(s string) {
			t.Error(s)
		}); err != nil {
			t.Error(err)
		}

		if err := cmd.Wait(); err != nil {
			if out := stderr.String(); len(out) > 0 {
				t.Fatalf("err=%s, stderr=%s", err, out)
			}
		}

		if t.Failed() {
			args := append([]string(nil), cmd.Args[1:len(cmd.Args)-1]...)
			args = append(args, "-w", pkg.Dir)
			for i := range args {
				args[i] = strconv.Quote(args[i])
			}
			t.Logf("run the following to fix your formatting:\n"+
				"\n%s %s\n\n"+
				"Don't forget to add amend the result to the correct commits.",
				cmd.Args[0], strings.Join(args, " "),
			)
		}
	})

	t.Run("TestVet", func(t *testing.T) {
		t.Parallel()
		// `go tool vet` is a special snowflake that emits all its output on
		// `stderr.
		cmd := exec.Command("go", "tool", "vet", "-all", "-shadow", "-printfuncs",
			strings.Join([]string{
				"Info:1",
				"Infof:1",
				"InfofDepth:2",
				"Warning:1",
				"Warningf:1",
				"WarningfDepth:2",
				"Error:1",
				"Errorf:1",
				"ErrorfDepth:2",
				"Fatal:1",
				"Fatalf:1",
				"FatalfDepth:2",
				"Event:1",
				"Eventf:1",
				"ErrEvent:1",
				"ErrEventf:1",
				"VEvent:2",
				"VEventf:2",
				"UnimplementedWithIssueErrorf:1",
			}, ","),
			".",
		)
		cmd.Dir = pkg.Dir
		var b bytes.Buffer
		cmd.Stdout = &b
		cmd.Stderr = &b
		switch err := cmd.Run(); err.(type) {
		case nil:
		case *exec.ExitError:
			// Non-zero exit is expected.
		default:
			t.Fatal(err)
		}

		if err := stream.ForEach(stream.Sequence(
			stream.FilterFunc(func(arg stream.Arg) error {
				scanner := bufio.NewScanner(&b)
				for scanner.Scan() {
					arg.Out <- scanner.Text()
				}
				return scanner.Err()
			}),
			stream.GrepNot(`declaration of "?(pE|e)rr"? shadows`),
			stream.GrepNot(`\.pb\.gw\.go:[0-9]+: declaration of "?ctx"? shadows`),
		), func(s string) {
			t.Error(s)
		}); err != nil {
			t.Error(err)
		}
	})

	t.Run("TestForbiddenImports", func(t *testing.T) {
		t.Parallel()
		filter := stream.FilterFunc(func(arg stream.Arg) error {
			for _, useAllFiles := range []bool{false, true} {
				buildContext := build.Default
				buildContext.CgoEnabled = true
				buildContext.UseAllFiles = useAllFiles
			outer:
				for path := range buildutil.ExpandPatterns(&buildContext, []string{cockroachDB + "/..."}) {
					importPkg, err := buildContext.Import(path, pkg.Dir, 0)
					switch err.(type) {
					case nil:
						for _, s := range importPkg.Imports {
							arg.Out <- importPkg.ImportPath + ": " + s
						}
						for _, s := range importPkg.TestImports {
							arg.Out <- importPkg.ImportPath + ": " + s
						}
						for _, s := range importPkg.XTestImports {
							arg.Out <- importPkg.ImportPath + ": " + s
						}
					case *build.NoGoError:
					case *build.MultiplePackageError:
						if useAllFiles {
							continue outer
						}
					default:
						return errors.Wrapf(err, "error loading package %s", path)
					}
				}
			}
			return nil
		})
		settingsPkgPrefix := `github.com/cockroachdb/cockroach/pkg/settings`
		if err := stream.ForEach(stream.Sequence(
			filter,
			stream.Sort(),
			stream.Uniq(),
			stream.GrepNot(`cockroach/pkg/cmd/`),
			stream.Grep(`^`+settingsPkgPrefix+`: | (github\.com/golang/protobuf/proto|github\.com/satori/go\.uuid|log|path|context|syscall)$`),
			stream.GrepNot(`cockroach/pkg/(cli|security): syscall$`),
			stream.GrepNot(`cockroach/pkg/(base|security|util/(log|randutil|stop)): log$`),
			stream.GrepNot(`cockroach/pkg/(server/serverpb|ts/tspb): github\.com/golang/protobuf/proto$`),
			stream.GrepNot(`cockroach/pkg/util/caller: path$`),
			stream.GrepNot(`cockroach/pkg/util/uuid: github\.com/satori/go\.uuid$`),
		), func(s string) {
			switch {
			case strings.HasSuffix(s, " path"):
				t.Errorf(`%s <- please use "path/filepath" instead of "path"`, s)
			case strings.HasSuffix(s, " log"):
				t.Errorf(`%s <- please use "util/log" instead of "log"`, s)
			case strings.HasSuffix(s, " github.com/golang/protobuf/proto"):
				t.Errorf(`%s <- please use "github.com/gogo/protobuf/proto" instead of "github.com/golang/protobuf/proto"`, s)
			case strings.HasSuffix(s, " github.com/satori/go.uuid"):
				t.Errorf(`%s <- please use "util/uuid" instead of "github.com/satori/go.uuid"`, s)
			case strings.HasSuffix(s, " context"):
				t.Errorf(`%s <- please use "golang.org/x/net/context" instead of "context"`, s)
			case strings.HasSuffix(s, " syscall"):
				t.Errorf(`%s <- please use "golang.org/x/sys" instead of "syscall"`, s)
			case strings.HasPrefix(s, settingsPkgPrefix+": github.com/cockroachdb/cockroach"):
				if !strings.HasSuffix(s, "testutils") && !strings.HasSuffix(s, "humanizeutil") &&
					!strings.HasSuffix(s, settingsPkgPrefix) {
					t.Errorf("%s <- please don't add CRDB dependencies to settings pkg", s)
				}
			}
		}); err != nil {
			t.Error(err)
		}
	})

	// Things that are packaged scoped are below here.
	pkgScope, ok := os.LookupEnv("PKG")
	if !ok {
		pkgScope = "./..."
	}

	// TODO(tamird): replace this with errcheck.NewChecker() when
	// https://github.com/dominikh/go-tools/issues/57 is fixed.
	t.Run("TestErrCheck", func(t *testing.T) {
		// errcheck uses 1GB of ram (as of 2017-02-18), so don't parallelize it.
		cmd, stderr, filter, err := dirCmd(
			pkg.Dir,
			"errcheck",
			"-exclude",
			filepath.Join(filepath.Dir(pkg.Dir), "build", "errcheck_excludes.txt"),
			pkgScope,
		)
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		if err := stream.ForEach(filter, func(s string) {
			t.Errorf(`%s <- unchecked error`, s)
		}); err != nil {
			t.Error(err)
		}

		if err := cmd.Wait(); err != nil {
			if out := stderr.String(); len(out) > 0 {
				t.Fatalf("err=%s, stderr=%s", err, out)
			}
		}
	})

	t.Run("TestReturnCheck", func(t *testing.T) {
		// returncheck uses 1GB of ram (as of 2017-02-18), so don't parallelize it.
		cmd, stderr, filter, err := dirCmd(pkg.Dir, "returncheck", pkgScope)
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		if err := stream.ForEach(filter, func(s string) {
			t.Errorf(`%s <- unchecked error`, s)
		}); err != nil {
			t.Error(err)
		}

		if err := cmd.Wait(); err != nil {
			if out := stderr.String(); len(out) > 0 {
				t.Fatalf("err=%s, stderr=%s", err, out)
			}
		}
	})

	t.Run("TestGolint", func(t *testing.T) {
		t.Parallel()
		cmd, stderr, filter, err := dirCmd(pkg.Dir, "golint", pkgScope)
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		if err := stream.ForEach(stream.Sequence(
			filter,
			stream.GrepNot(`((\.pb|\.pb\.gw|embedded|_string)\.go|sql/parser/(yaccpar|sql\.y):)`),
		), func(s string) {
			t.Error(s)
		}); err != nil {
			t.Error(err)
		}

		if err := cmd.Wait(); err != nil {
			if out := stderr.String(); len(out) > 0 {
				t.Fatalf("err=%s, stderr=%s", err, out)
			}
		}
	})

	t.Run("TestUnconvert", func(t *testing.T) {
		if testing.Short() {
			t.Skip("short flag")
		}
		t.Parallel()
		cmd, stderr, filter, err := dirCmd(pkg.Dir, "unconvert", pkgScope)
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		if err := stream.ForEach(stream.Sequence(
			filter,
			stream.GrepNot(`\.pb\.go:`),
		), func(s string) {
			t.Error(s)
		}); err != nil {
			t.Error(err)
		}

		if err := cmd.Wait(); err != nil {
			if out := stderr.String(); len(out) > 0 {
				t.Fatalf("err=%s, stderr=%s", err, out)
			}
		}
	})

	t.Run("TestMetacheck", func(t *testing.T) {
		if testing.Short() {
			t.Skip("short flag")
		}
		// metacheck uses 2.5GB of ram (as of 2017-02-18), so don't parallelize it.
		cmd, stderr, filter, err := dirCmd(
			pkg.Dir,
			"metacheck",
			"-ignore",

			strings.Join([]string{
				"github.com/cockroachdb/cockroach/pkg/security/securitytest/embedded.go:S1013",
				"github.com/cockroachdb/cockroach/pkg/ui/embedded.go:S1013",

				// Intentionally compare an unsigned integer <= 0 to avoid knowledge
				// of the type at the caller and for consistency with convention.
				"github.com/cockroachdb/cockroach/pkg/storage/replica.go:SA4003",
				// Allow a comment to refer to an "unused" argument.
				//
				// TODO(bdarnell): remove when/if #8360 is fixed.
				"github.com/cockroachdb/cockroach/pkg/storage/intent_resolver.go:SA4009",
				// The generated parser is full of `case` arms such as:
				//
				// case 1:
				// 	sqlDollar = sqlS[sqlpt-1 : sqlpt+1]
				// 	//line sql.y:781
				// 	{
				// 		sqllex.(*Scanner).stmts = sqlDollar[1].union.stmts()
				// 	}
				//
				// where the code in braces is generated from the grammar action; if
				// the action does not make use of the matched expression, sqlDollar
				// will be assigned but not used. This is expected and intentional.
				//
				// Concretely, the grammar:
				//
				// stmt:
				//   alter_table_stmt
				// | backup_stmt
				// | copy_from_stmt
				// | create_stmt
				// | delete_stmt
				// | drop_stmt
				// | explain_stmt
				// | help_stmt
				// | prepare_stmt
				// | execute_stmt
				// | deallocate_stmt
				// | grant_stmt
				// | insert_stmt
				// | rename_stmt
				// | revoke_stmt
				// | savepoint_stmt
				// | select_stmt
				//   {
				//     $$.val = $1.slct()
				//   }
				// | set_stmt
				// | show_stmt
				// | split_stmt
				// | transaction_stmt
				// | release_stmt
				// | truncate_stmt
				// | update_stmt
				// | /* EMPTY */
				//   {
				//     $$.val = Statement(nil)
				//   }
				//
				// is compiled into the `case` arm:
				//
				// case 28:
				// 	sqlDollar = sqlS[sqlpt-0 : sqlpt+1]
				// 	//line sql.y:830
				// 	{
				// 		sqlVAL.union.val = Statement(nil)
				// 	}
				//
				// which results in the unused warning:
				//
				// sql/parser/yaccpar:362:3: this value of sqlDollar is never used (SA4006)
				"github.com/cockroachdb/cockroach/pkg/sql/parser/sql.go:SA4006",
				// sql/parser/yaccpar:14:6: type sqlParser is unused (U1000)
				// sql/parser/yaccpar:15:2: func sqlParser.Parse is unused (U1000)
				// sql/parser/yaccpar:16:2: func sqlParser.Lookahead is unused (U1000)
				// sql/parser/yaccpar:29:6: func sqlNewParser is unused (U1000)
				// sql/parser/yaccpar:152:6: func sqlParse is unused (U1000)
				"github.com/cockroachdb/cockroach/pkg/sql/parser/sql.go:U1000",
				// Generated file containing many unused postgres error codes.
				"github.com/cockroachdb/cockroach/pkg/sql/pgwire/pgerror/codes.go:U1000",
				// Deprecated database/sql/driver interfaces not compatible with 1.7.
				"github.com/cockroachdb/cockroach/pkg/sql/*.go:SA1019",
				"github.com/cockroachdb/cockroach/pkg/cli/sql_util.go:SA1019",
			}, " "),
			// NB: this doesn't use `pkgScope` because `honnef.co/go/unused`
			// produces many false positives unless it inspects all our packages.
			"./...",
		)
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		if err := stream.ForEach(stream.Sequence(
			filter,
			stream.GrepNot(`: (field no|type No)Copy is unused \(U1000\)$`),
		), func(s string) {
			t.Error(s)
		}); err != nil {
			t.Error(err)
		}

		if err := cmd.Wait(); err != nil {
			if out := stderr.String(); len(out) > 0 {
				t.Fatalf("err=%s, stderr=%s", err, out)
			}
		}
	})
}
