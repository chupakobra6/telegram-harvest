package mtproto

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestAccountFoldersOutputContainsIdentitiesWithoutAccessHashes(t *testing.T) {
	const secret = int64(987654321012345)
	filters := []tg.DialogFilterClass{
		&tg.DialogFilterDefault{},
		&tg.DialogFilter{ID: 3, Title: tg.TextWithEntities{Text: CompletedWorkFolder}, Contacts: true, ExcludeArchived: true,
			IncludePeers: []tg.InputPeerClass{&tg.InputPeerUser{UserID: 11, AccessHash: secret}, &tg.InputPeerChat{ChatID: 22}},
			PinnedPeers:  []tg.InputPeerClass{&tg.InputPeerChannel{ChannelID: 33, AccessHash: secret}},
			ExcludePeers: []tg.InputPeerClass{&tg.InputPeerUser{UserID: 44, AccessHash: secret}}},
		&tg.DialogFilterChatlist{ID: 5, Title: tg.TextWithEntities{Text: "Shared"}, IncludePeers: []tg.InputPeerClass{&tg.InputPeerUser{UserID: 55, AccessHash: secret}}},
	}
	views := folderViews(filters)
	encoded, err := json.Marshal(AccountFoldersResult{AccountID: 99, Filters: views})
	if err != nil || strings.Contains(strings.ToLower(string(encoded)), "accesshash") || strings.Contains(strings.ToLower(string(encoded)), "access_hash") || strings.Contains(string(encoded), "987654321012345") {
		t.Fatal("folder output leaked access hashes", string(encoded), err)
	}
	if len(views) != 3 || views[1].ID != 3 || views[1].IncludePeers[0].ID != 11 || views[1].IncludePeers[1].ID != 22 || views[1].PinnedPeers[0].ID != 33 || views[1].ExcludePeers[0].ID != 44 || !views[1].Settings["exclude_archived"] || views[2].Kind != "shared" {
		t.Fatal("safe projection lost order, identities or filter flags", views)
	}
}

type organizationFake struct {
	selfID         int64
	filters        []tg.DialogFilterClass
	dialogs        map[int64]*tg.Dialog
	dialogReads    map[int64]int
	filterWrites   int
	archiveWrites  []int64
	failArchive    int64
	staleUser      int64
	staleRead      int
	ignoreArchive  bool
	loseMembership bool
	updateErr      error
	filterReads    int
	beforeFresh    func()
	corruptField   string
}

func organizationFixture() (AccountOrganizeManifest, *organizationFake) {
	m := AccountOrganizeManifest{Profile: "work", AccountID: 99, FolderName: CompletedWorkFolder,
		Peers: []AccountOrganizePeer{{UserID: 11, LastMessageID: 10}, {UserID: 22, LastMessageID: 20}}}
	f := &organizationFake{selfID: 99, dialogs: map[int64]*tg.Dialog{}, dialogReads: map[int64]int{}}
	for _, p := range m.Peers {
		f.dialogs[p.UserID] = &tg.Dialog{Peer: &tg.PeerUser{UserID: p.UserID}, TopMessage: p.LastMessageID, UnreadCount: 5, ReadInboxMaxID: 2, ReadOutboxMaxID: 3}
	}
	return m, f
}

func (f *organizationFake) SelfProfile(context.Context) (harvest.SelfProfile, error) {
	return harvest.SelfProfile{ID: f.selfID}, nil
}
func (f *organizationFake) organizationFilters(context.Context) ([]tg.DialogFilterClass, error) {
	f.filterReads++
	if f.filterReads == 2 && f.beforeFresh != nil {
		f.beforeFresh()
	}
	return f.filters, nil
}
func (f *organizationFake) organizationUsers(_ context.Context, peers []AccountOrganizePeer) (map[int64]*tg.InputPeerUser, error) {
	users := map[int64]*tg.InputPeerUser{}
	for _, p := range peers {
		users[p.UserID] = &tg.InputPeerUser{UserID: p.UserID, AccessHash: p.UserID * 100}
	}
	return users, nil
}
func (f *organizationFake) organizationDialog(_ context.Context, peer *tg.InputPeerUser) (*tg.Dialog, error) {
	f.dialogReads[peer.UserID]++
	if peer.UserID == f.staleUser && f.dialogReads[peer.UserID] >= f.staleRead {
		f.dialogs[peer.UserID].TopMessage++
	}
	copy := *f.dialogs[peer.UserID]
	return &copy, nil
}
func (f *organizationFake) organizationUpdateFilter(_ context.Context, filter *tg.DialogFilter) error {
	f.filterWrites++
	var encoded bin.Buffer
	if err := filter.Encode(&encoded); err != nil {
		return err
	}
	server, err := tg.DecodeDialogFilter(&encoded)
	if err != nil {
		return err
	}
	copy := server.(*tg.DialogFilter)
	if f.corruptField == "flag" {
		copy.ExcludeMuted = false
		copy.Flags.Unset(11)
	} else if f.corruptField == "peer" {
		copy.IncludePeers = copy.IncludePeers[1:]
	}
	for i, old := range f.filters {
		if id, _ := filterIdentity(old); id == filter.ID {
			f.filters[i] = copy
			return f.updateErr
		}
	}
	f.filters = append(f.filters, copy)
	return f.updateErr
}

func TestAccountOrganizePreservesConcurrentFilterEditsAndOccupiedID(t *testing.T) {
	for _, scenario := range []string{"edited", "occupied-id"} {
		t.Run(scenario, func(t *testing.T) {
			m, f := organizationFixture()
			if scenario == "edited" {
				f.filters = []tg.DialogFilterClass{&tg.DialogFilter{ID: 2, Title: tg.TextWithEntities{Text: CompletedWorkFolder}}}
			}
			concurrent := &tg.DialogFilter{ID: 2, Title: tg.TextWithEntities{Text: "Other client"}, ExcludeMuted: true, IncludePeers: []tg.InputPeerClass{&tg.InputPeerUser{UserID: 88, AccessHash: 9}}}
			if scenario == "edited" {
				concurrent.Title.Text = CompletedWorkFolder
			}
			f.beforeFresh = func() { f.filters = []tg.DialogFilterClass{concurrent} }
			result, err := organizeAccount(context.Background(), f, m, true)
			if err != nil || !result.Complete {
				t.Fatal("fresh server state not merged", result, err)
			}
			first := f.filters[0].(*tg.DialogFilter)
			if !first.ExcludeMuted || !containsUser(first.IncludePeers, 88) {
				t.Fatal("concurrent field or include lost", first)
			}
			if scenario == "occupied-id" && (result.FolderID != 3 || first.Title.Text != "Other client" || len(first.IncludePeers) != 1) {
				t.Fatal("newly occupied ID was overwritten", result, first)
			}
		})
	}
}

func TestAccountOrganizeReadbackRejectsLostPreservedFields(t *testing.T) {
	for _, lost := range []string{"flag", "peer"} {
		t.Run(lost, func(t *testing.T) {
			m, f := organizationFixture()
			f.corruptField = lost
			f.filters = []tg.DialogFilterClass{&tg.DialogFilter{ID: 2, Title: tg.TextWithEntities{Text: CompletedWorkFolder}, ExcludeMuted: true, IncludePeers: []tg.InputPeerClass{&tg.InputPeerUser{UserID: 88, AccessHash: 9}}}}
			result, err := organizeAccount(context.Background(), f, m, true)
			if err == nil || result.Complete || len(f.archiveWrites) != 0 {
				t.Fatal("target membership hid lost preserved metadata", result, err)
			}
		})
	}
}
func (f *organizationFake) organizationArchive(_ context.Context, peer *tg.InputPeerUser) error {
	f.archiveWrites = append(f.archiveWrites, peer.UserID)
	if peer.UserID == f.failArchive {
		return errors.New("archive transport failure")
	}
	if !f.ignoreArchive {
		f.dialogs[peer.UserID].FolderID = 1
	}
	if f.loseMembership {
		f.filters = nil
	}
	return nil
}

func TestAccountOrganizeManifestPolicy(t *testing.T) {
	for _, change := range []string{"profile", "account", "folder", "empty", "duplicate", "self", "negative", "missing-head"} {
		t.Run(change, func(t *testing.T) {
			m, _ := organizationFixture()
			switch change {
			case "profile":
				m.Profile = "other"
			case "account":
				m.AccountID++
			case "folder":
				m.FolderName = "Other"
			case "empty":
				m.Peers = nil
			case "duplicate":
				m.Peers = append(m.Peers, m.Peers[0])
			case "self":
				m.Peers[0].UserID = m.AccountID
			case "negative":
				m.Peers[0].UserID = -1
			case "missing-head":
				m.Peers[0].LastMessageID = 0
			}
			if err := ValidateAccountOrganizeManifest(m, "work", 99); err == nil {
				t.Fatal("unsafe manifest accepted", change)
			}
		})
	}
}

func TestAccountOrganizeFolderMergePreservesEveryExistingField(t *testing.T) {
	m, f := organizationFixture()
	users, _ := f.organizationUsers(context.Background(), m.Peers)
	existing := &tg.DialogFilter{ID: 7, Title: tg.TextWithEntities{Text: CompletedWorkFolder, Entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 2}}},
		Contacts: true, NonContacts: true, Groups: true, Broadcasts: true, Bots: true, ExcludeMuted: true, ExcludeRead: true, ExcludeArchived: true, TitleNoanimate: true,
		PinnedPeers: []tg.InputPeerClass{users[11]}, IncludePeers: []tg.InputPeerClass{&tg.InputPeerChat{ChatID: 5}}, ExcludePeers: []tg.InputPeerClass{&tg.InputPeerChannel{ChannelID: 8, AccessHash: 9}}}
	existing.SetEmoticon("✅")
	existing.SetColor(3)
	before := *existing
	before.IncludePeers = append([]tg.InputPeerClass(nil), existing.IncludePeers...)
	merged, changed, err := mergeCompletedWorkFolder([]tg.DialogFilterClass{existing}, m.Peers, users)
	if err != nil || !changed || len(merged.IncludePeers) != 2 || !containsUser(merged.IncludePeers, 22) || containsUser(merged.IncludePeers, 11) {
		t.Fatal("merge lost existing includes or duplicated a pinned user", merged, changed, err)
	}
	if !reflect.DeepEqual(existing, &before) {
		t.Fatal("merge mutated original server filter")
	}
	merged.IncludePeers = before.IncludePeers
	if !reflect.DeepEqual(merged, &before) {
		t.Fatal("merge changed another filter field", merged, before)
	}
}

func TestAccountOrganizeRejectsAmbiguousSharedAndExcludedFolder(t *testing.T) {
	m, f := organizationFixture()
	users, _ := f.organizationUsers(context.Background(), m.Peers)
	for _, filters := range [][]tg.DialogFilterClass{
		{&tg.DialogFilterChatlist{ID: 2, Title: tg.TextWithEntities{Text: CompletedWorkFolder}}},
		{&tg.DialogFilter{ID: 2, Title: tg.TextWithEntities{Text: CompletedWorkFolder}}, &tg.DialogFilter{ID: 3, Title: tg.TextWithEntities{Text: CompletedWorkFolder}}},
		{&tg.DialogFilter{ID: 2, Title: tg.TextWithEntities{Text: CompletedWorkFolder}, ExcludePeers: []tg.InputPeerClass{users[11]}}},
	} {
		if _, _, err := mergeCompletedWorkFolder(filters, m.Peers, users); err == nil {
			t.Fatal("unsafe folder accepted", filters)
		}
	}
}

func TestAccountOrganizeDryRunAndIdempotentServerVerification(t *testing.T) {
	m, f := organizationFixture()
	f.filters = []tg.DialogFilterClass{&tg.DialogFilter{ID: 2, Title: tg.TextWithEntities{Text: "Untouched"}}}
	before := *f.dialogs[11]
	result, err := organizeAccount(context.Background(), f, m, false)
	if err != nil || !result.DryRun || !result.Ready || result.Complete || result.FolderID != 3 || f.filterWrites != 0 || len(f.archiveWrites) != 0 || result.Peers[0].Verified {
		t.Fatal("dry-run mutated or reported applied completion", result, err)
	}
	result, err = organizeAccount(context.Background(), f, m, true)
	if err != nil || !result.Complete || result.DryRun || f.filterWrites != 1 || !reflect.DeepEqual(f.archiveWrites, []int64{11, 22}) {
		t.Fatal("apply not server verified", result, err, f)
	}
	for _, row := range result.Peers {
		if !row.Verified || !row.Archived || !row.InFolder {
			t.Fatal("complete without individual server proof", row)
		}
	}
	after := *f.dialogs[11]
	after.FolderID = before.FolderID
	if !reflect.DeepEqual(before, after) {
		t.Fatal("archive changed read or pin metadata", before, after)
	}
	result, err = organizeAccount(context.Background(), f, m, true)
	if err != nil || !result.Complete || f.filterWrites != 1 || len(f.archiveWrites) != 2 {
		t.Fatal("repeat did not reuse existing folder/archive state", result, err, f)
	}
}

func TestAccountOrganizePartialFailureKeepsVerifiedRowsAndResumes(t *testing.T) {
	m, f := organizationFixture()
	f.failArchive = 22
	result, err := organizeAccount(context.Background(), f, m, true)
	if err == nil || result.Complete || !result.Peers[0].Verified || result.Peers[1].Verified || result.Peers[1].Error == "" || !result.Peers[1].InFolder {
		t.Fatal("partial result was lost or called complete", result, err)
	}
	f.failArchive = 0
	result, err = organizeAccount(context.Background(), f, m, true)
	if err != nil || !result.Complete || f.filterWrites != 1 || !reflect.DeepEqual(f.archiveWrites, []int64{11, 22, 22}) {
		t.Fatal("partial retry repeated a confirmed write", result, err, f.archiveWrites)
	}
}

func TestAccountOrganizeStaleHeadsAndAccountMismatch(t *testing.T) {
	for _, scenario := range []string{"account", "initial-head", "later-head", "pinned"} {
		t.Run(scenario, func(t *testing.T) {
			m, f := organizationFixture()
			switch scenario {
			case "account":
				f.selfID++
			case "initial-head":
				f.staleUser, f.staleRead = 22, 1
			case "later-head":
				f.staleUser, f.staleRead = 22, 2
			case "pinned":
				f.dialogs[22].Pinned = true
			}
			result, err := organizeAccount(context.Background(), f, m, true)
			if err == nil || result.Complete {
				t.Fatal("unsafe dialog accepted", result, err)
			}
			if scenario == "later-head" {
				if !result.Peers[0].Verified || !reflect.DeepEqual(f.archiveWrites, []int64{11}) {
					t.Fatal("new reply was archived or previous proof lost", result, f.archiveWrites)
				}
			} else if f.filterWrites != 0 || len(f.archiveWrites) != 0 {
				t.Fatal("preflight failure changed metadata", f)
			}
		})
	}
}

func TestAccountOrganizeRequiresBothServerReadbacks(t *testing.T) {
	for _, scenario := range []string{"archive-missing", "folder-disappeared", "unknown-filter-write"} {
		t.Run(scenario, func(t *testing.T) {
			m, f := organizationFixture()
			f.ignoreArchive = scenario == "archive-missing"
			f.loseMembership = scenario == "folder-disappeared"
			if scenario == "unknown-filter-write" {
				f.updateErr = errors.New("interrupted after write")
			}
			result, err := organizeAccount(context.Background(), f, m, true)
			if err == nil || result.Complete || result.Peers[0].Verified {
				t.Fatal("missing server proof reported as success", result, err)
			}
			if f.updateErr != nil {
				if len(f.archiveWrites) != 0 {
					t.Fatal("continued after uncertain folder write")
				}
				f.updateErr = nil
				result, err = organizeAccount(context.Background(), f, m, true)
				if err != nil || !result.Complete || f.filterWrites != 1 {
					t.Fatal("uncertain folder write not recovered idempotently", result, err)
				}
			}
		})
	}
}
