package agent

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// botDoorOpts opens chat c1, written by write, through the chat door on a fresh or a resumed session, and returns
// what the door sent. HOME is a test directory, so workspace.KiroHome() is under it.
func botDoorOpts(t *testing.T, resumed bool, write func(c *marotte.Chat), opts ...Option) (*marotte.SessionOpts, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), "/tmp/work", fakeEngineOf(func() engineSession { return newFakeBridge() }, nil), cs, opts...)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	h.SetChatSteering(func(context.Context) []marotte.ClientSteeringDoc { return []marotte.ClientSteeringDoc{chatDoc} })
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "Weekly answers"
		if resumed {
			c.ACPSessionID = "sess_bot_resume"
		}
		write(c)
		return true
	})
	sb, err := h.coord.openReleased(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("open the chat: %v", err)
	}
	br, ok := unguarded(sb.session()).(*fakeBridge)
	if !ok {
		t.Fatalf("the chat's bridge is %T, want the fake", sb.session())
	}
	sent := br.lastSessionOpts()
	if sent == nil {
		t.Fatal("the session was never opened")
	}
	return sent, filepath.Join(home, ".kiro")
}

var chatDoc = marotte.ClientSteeringDoc{Name: "marotte", Inclusion: "always", Content: "chat steering"}

var doors = []struct {
	name    string
	resumed bool
}{{"a new session", false}, {"a loaded session", true}}

func TestChatSessionOpts_AStartedBotCarriesItsRoleSteeringAndMode(t *testing.T) {
	for _, door := range doors {
		t.Run(door.name, func(t *testing.T) {
			opts, kiroHome := botDoorOpts(t, door.resumed, func(c *marotte.Chat) {
				c.CurrentModeID = "kiro_spec"
				c.Bot = &marotte.Bot{Goal: "Answer the weekly questions.", Steering: "Answer in French.", StartedAt: 1, Memory: true}
			})
			if len(opts.CustomAgents) != 1 || opts.CustomAgents[0].ID != marotte.BotRoleID {
				t.Fatalf("CustomAgents = %+v, want the one Bot role", opts.CustomAgents)
			}
			memory := filepath.Join(kiroHome, "bots", "c1", "memory.md")
			rules := opts.CustomAgents[0].Permissions.Rules
			if last := rules[len(rules)-1]; last.Capability != "fs_write" || !slices.Equal(last.Match, []string{memory}) {
				t.Errorf("the role's last rule = %+v, want the write of %s under workspace.KiroHome()", last, memory)
			}
			want := []marotte.ClientSteeringDoc{chatDoc, {Name: "bot-steering", Inclusion: "always", Content: "Answer in French."}}
			if !slices.Equal(opts.Steering, want) {
				t.Errorf("Steering = %+v, want the chat steering then the Bot's %+v", opts.Steering, want)
			}
			if opts.Mode != marotte.BotRoleID {
				t.Errorf("Mode = %q, want %q whatever the record says", opts.Mode, marotte.BotRoleID)
			}
		})
	}
}

func TestChatSessionOpts_ABotWithNoSteeringSendsOnlyTheChatSteering(t *testing.T) {
	opts, _ := botDoorOpts(t, false, func(c *marotte.Chat) {
		c.CurrentModeID = marotte.BotRoleID
		c.Bot = &marotte.Bot{Goal: "Answer the weekly questions.", StartedAt: 1}
	})
	if !slices.Equal(opts.Steering, []marotte.ClientSteeringDoc{chatDoc}) {
		t.Errorf("Steering = %+v, want only the chat steering", opts.Steering)
	}
	if len(opts.CustomAgents) != 1 {
		t.Errorf("CustomAgents = %+v, want the role", opts.CustomAgents)
	}
}

func TestChatSessionOpts_ADraftAndARegularChatCarryNeither(t *testing.T) {
	for _, tc := range []struct {
		name string
		bot  *marotte.Bot
	}{
		{"a draft", &marotte.Bot{Goal: "Not started yet.", Steering: "Answer in French."}},
		{"a regular chat", nil},
	} {
		for _, door := range doors {
			t.Run(tc.name+" on "+door.name, func(t *testing.T) {
				opts, _ := botDoorOpts(t, door.resumed, func(c *marotte.Chat) {
					c.CurrentModeID = "kiro_spec"
					c.Bot = tc.bot
				})
				if len(opts.CustomAgents) != 0 {
					t.Errorf("CustomAgents = %+v, want none", opts.CustomAgents)
				}
				if !slices.Equal(opts.Steering, []marotte.ClientSteeringDoc{chatDoc}) {
					t.Errorf("Steering = %+v, want only the chat steering", opts.Steering)
				}
				if opts.Mode != "kiro_spec" {
					t.Errorf("Mode = %q, want the record's", opts.Mode)
				}
			})
		}
	}
}

// TestChatSessionOpts_ARoleRecordThatIsNoStartedBotAsksForTheDefaultMode pins that only a started Bot's door asks for
// the role: a record naming it with no bot block, or a draft (whose record AdoptMode holds on the role), is sent no
// role, so its door asks for the default mode instead of one KAS never registered.
func TestChatSessionOpts_ARoleRecordThatIsNoStartedBotAsksForTheDefaultMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		bot  *marotte.Bot
	}{
		{"a record with no bot block", nil},
		{"a draft", &marotte.Bot{Goal: "Not started yet."}},
	} {
		for _, door := range doors {
			t.Run(tc.name+" on "+door.name, func(t *testing.T) {
				opts, _ := botDoorOpts(t, door.resumed, func(c *marotte.Chat) {
					c.CurrentModeID = marotte.BotRoleID
					c.Bot = tc.bot
				})
				if len(opts.CustomAgents) != 0 {
					t.Errorf("CustomAgents = %+v, want none", opts.CustomAgents)
				}
				if opts.Mode != marotte.DefaultModeID {
					t.Errorf("Mode = %q, want %q", opts.Mode, marotte.DefaultModeID)
				}
			})
		}
	}
}

// TestChatSessionOpts_AStartedBotWithMemoryListsItsDirectory pins that the door lists the Bot's directory in the
// session's additionalDirectories after the granted ones, so KAS treats the memory file as in a root, and no other
// chat's door lists it.
func TestChatSessionOpts_AStartedBotWithMemoryListsItsDirectory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		bot     *marotte.Bot
		withDir bool
	}{
		{"a started Bot with memory on", &marotte.Bot{Goal: "Answer the weekly questions.", StartedAt: 1, Memory: true}, true},
		{"a started Bot with memory off", &marotte.Bot{Goal: "Answer the weekly questions.", StartedAt: 1}, false},
		{"a draft with memory on", &marotte.Bot{Goal: "Not started yet.", Memory: true}, false},
		{"a regular chat", nil, false},
	} {
		for _, door := range doors {
			t.Run(tc.name+" on "+door.name, func(t *testing.T) {
				f := newAgentRootsFixture(t)
				opts, kiroHome := botDoorOpts(t, door.resumed, func(c *marotte.Chat) { c.Bot = tc.bot }, WithConfigDir(f.config))
				want := []string{f.extra}
				if tc.withDir {
					want = append(want, filepath.Join(kiroHome, "bots", "c1"))
				}
				if !slices.Equal(opts.AdditionalDirectories, want) {
					t.Errorf("AdditionalDirectories = %v, want %v", opts.AdditionalDirectories, want)
				}
			})
		}
	}
}
