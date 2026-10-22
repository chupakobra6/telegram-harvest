package mtproto

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

const CompletedWorkFolder = "Отработка"

type AccountOrganizePeer struct {
	UserID        int64 `json:"user_id"`
	LastMessageID int   `json:"last_message_id"`
}

type AccountOrganizeManifest struct {
	Profile    string                `json:"profile"`
	AccountID  int64                 `json:"account_id"`
	FolderName string                `json:"folder_name"`
	Peers      []AccountOrganizePeer `json:"peers"`
}

type AccountOrganizePeerResult struct {
	UserID        int64  `json:"user_id"`
	LastMessageID int    `json:"last_message_id"`
	InFolder      bool   `json:"in_folder"`
	Archived      bool   `json:"archived"`
	Verified      bool   `json:"server_verified"`
	Error         string `json:"error,omitempty"`
}

type AccountOrganizeResult struct {
	Profile    string                      `json:"profile"`
	AccountID  int64                       `json:"account_id"`
	FolderName string                      `json:"folder_name"`
	FolderID   int                         `json:"folder_id"`
	DryRun     bool                        `json:"dry_run"`
	Ready      bool                        `json:"ready"`
	Complete   bool                        `json:"complete"`
	Peers      []AccountOrganizePeerResult `json:"peers"`
}

type AccountFoldersResult struct {
	AccountID int64               `json:"account_id"`
	Filters   []AccountFolderView `json:"filters"`
}

// Public folder output carries peer identities only. Access hashes remain
// inside the live session for native filter merging and updates.
type AccountFolderPeer struct {
	Type string `json:"type"`
	ID   int64  `json:"id,omitempty"`
}

type AccountFolderView struct {
	ID           int                 `json:"id"`
	Title        string              `json:"title"`
	Kind         string              `json:"kind"`
	Flags        uint32              `json:"flags"`
	Settings     map[string]bool     `json:"settings,omitempty"`
	Emoticon     string              `json:"emoticon,omitempty"`
	Color        *int                `json:"color,omitempty"`
	PinnedPeers  []AccountFolderPeer `json:"pinned_peers"`
	IncludePeers []AccountFolderPeer `json:"include_peers"`
	ExcludePeers []AccountFolderPeer `json:"exclude_peers"`
}

func folderPeerViews(peers []tg.InputPeerClass) []AccountFolderPeer {
	result := []AccountFolderPeer{}
	for _, peer := range peers {
		switch p := peer.(type) {
		case *tg.InputPeerUser:
			result = append(result, AccountFolderPeer{Type: "user", ID: p.UserID})
		case *tg.InputPeerChat:
			result = append(result, AccountFolderPeer{Type: "basic_group", ID: p.ChatID})
		case *tg.InputPeerChannel:
			result = append(result, AccountFolderPeer{Type: "channel", ID: p.ChannelID})
		case *tg.InputPeerSelf:
			result = append(result, AccountFolderPeer{Type: "self"})
		default:
			result = append(result, AccountFolderPeer{Type: "unsupported"})
		}
	}
	return result
}

func folderViews(filters []tg.DialogFilterClass) []AccountFolderView {
	result := []AccountFolderView{}
	for _, filter := range filters {
		id, title := filterIdentity(filter)
		row := AccountFolderView{ID: id, Title: title, Kind: "unsupported", PinnedPeers: []AccountFolderPeer{}, IncludePeers: []AccountFolderPeer{}, ExcludePeers: []AccountFolderPeer{}}
		switch f := filter.(type) {
		case *tg.DialogFilter:
			row.Kind, row.Flags, row.Emoticon = "ordinary", uint32(f.Flags), f.Emoticon
			if color, ok := f.GetColor(); ok {
				row.Color = &color
			}
			row.Settings = map[string]bool{"contacts": f.Contacts, "non_contacts": f.NonContacts, "groups": f.Groups, "broadcasts": f.Broadcasts, "bots": f.Bots, "exclude_muted": f.ExcludeMuted, "exclude_read": f.ExcludeRead, "exclude_archived": f.ExcludeArchived, "title_noanimate": f.TitleNoanimate}
			row.PinnedPeers, row.IncludePeers, row.ExcludePeers = folderPeerViews(f.PinnedPeers), folderPeerViews(f.IncludePeers), folderPeerViews(f.ExcludePeers)
		case *tg.DialogFilterChatlist:
			row.Kind, row.Flags, row.Emoticon = "shared", uint32(f.Flags), f.Emoticon
			if color, ok := f.GetColor(); ok {
				row.Color = &color
			}
			row.Settings = map[string]bool{"has_my_invites": f.HasMyInvites, "title_noanimate": f.TitleNoanimate}
			row.PinnedPeers, row.IncludePeers = folderPeerViews(f.PinnedPeers), folderPeerViews(f.IncludePeers)
		case *tg.DialogFilterDefault:
			row.Kind = "default"
		}
		result = append(result, row)
	}
	return result
}

func ValidateAccountOrganizeManifest(m AccountOrganizeManifest, profile string, boundID int64) error {
	if profile == "" || m.Profile != profile || boundID <= 0 || m.AccountID != boundID {
		return fmt.Errorf("manifest profile/account_id must match the selected bound account")
	}
	if m.FolderName != CompletedWorkFolder {
		return fmt.Errorf("account-organize supports only folder %q", CompletedWorkFolder)
	}
	if len(m.Peers) == 0 || len(m.Peers) > 100 {
		return fmt.Errorf("manifest must contain 1..100 exact user peers")
	}
	seen := map[int64]bool{}
	for _, p := range m.Peers {
		if p.UserID <= 0 || p.UserID == m.AccountID || p.LastMessageID <= 0 || seen[p.UserID] {
			return fmt.Errorf("unique positive user_id and last_message_id required; self is excluded")
		}
		seen[p.UserID] = true
	}
	return nil
}

type accountOrganizationSource interface {
	SelfProfile(context.Context) (harvest.SelfProfile, error)
	organizationFilters(context.Context) ([]tg.DialogFilterClass, error)
	organizationUsers(context.Context, []AccountOrganizePeer) (map[int64]*tg.InputPeerUser, error)
	organizationDialog(context.Context, *tg.InputPeerUser) (*tg.Dialog, error)
	organizationUpdateFilter(context.Context, *tg.DialogFilter) error
	organizationArchive(context.Context, *tg.InputPeerUser) error
}

func (s *Session) AccountFolders(ctx context.Context, expectedAccountID int64) (AccountFoldersResult, error) {
	self, err := s.SelfProfile(ctx)
	if err != nil {
		return AccountFoldersResult{}, err
	}
	if expectedAccountID <= 0 || self.ID != expectedAccountID {
		return AccountFoldersResult{}, fmt.Errorf("authorized account differs from the bound account")
	}
	filters, err := s.organizationFilters(ctx)
	return AccountFoldersResult{AccountID: self.ID, Filters: folderViews(filters)}, err
}

func (s *Session) OrganizeAccount(ctx context.Context, m AccountOrganizeManifest, apply bool) (AccountOrganizeResult, error) {
	return organizeAccount(ctx, s, m, apply)
}

func organizeAccount(ctx context.Context, source accountOrganizationSource, m AccountOrganizeManifest, apply bool) (AccountOrganizeResult, error) {
	result := AccountOrganizeResult{Profile: m.Profile, AccountID: m.AccountID, FolderName: m.FolderName, DryRun: !apply}
	if err := ValidateAccountOrganizeManifest(m, m.Profile, m.AccountID); err != nil {
		return result, err
	}
	for _, p := range m.Peers {
		result.Peers = append(result.Peers, AccountOrganizePeerResult{UserID: p.UserID, LastMessageID: p.LastMessageID})
	}
	self, err := source.SelfProfile(ctx)
	if err != nil {
		return result, err
	}
	if self.ID != m.AccountID {
		return result, fmt.Errorf("authorized account differs from manifest account_id")
	}
	users, err := source.organizationUsers(ctx, m.Peers)
	if err != nil {
		return result, err
	}
	filters, err := source.organizationFilters(ctx)
	if err != nil {
		return result, err
	}
	folder, changed, err := mergeCompletedWorkFolder(filters, m.Peers, users)
	if err != nil {
		return result, err
	}
	result.FolderID = folder.ID
	// Inspect every exact head before the first mutation, including dry-run.
	for i, p := range m.Peers {
		dialog, err := source.organizationDialog(ctx, users[p.UserID])
		if err == nil {
			err = organizationHeadCheck(dialog, p)
		}
		if err != nil {
			result.Peers[i].Error = err.Error()
			return result, err
		}
		result.Peers[i].Archived = dialog.FolderID == 1
		result.Peers[i].InFolder = completedWorkMembership(filters, folder.ID, p.UserID)
	}
	result.Ready = true
	if !apply {
		return result, nil
	}
	// Preserve edits made by another client during the head preflight.
	filters, err = source.organizationFilters(ctx)
	if err != nil {
		return result, err
	}
	folder, changed, err = mergeCompletedWorkFolder(filters, m.Peers, users)
	if err != nil {
		return result, err
	}
	result.FolderID = folder.ID
	if changed {
		if err := source.organizationUpdateFilter(ctx, folder); err != nil {
			return result, fmt.Errorf("folder update not confirmed; inspect before retry: %w", err)
		}
	}
	// Verify the actual server filter, never infer membership from the write.
	filters, err = source.organizationFilters(ctx)
	if err != nil {
		return result, err
	}
	if err := verifyCompletedWorkFilter(filters, folder); err != nil {
		return result, err
	}
	for i, p := range m.Peers {
		result.Peers[i].InFolder = completedWorkMembership(filters, folder.ID, p.UserID)
		if !result.Peers[i].InFolder {
			result.Peers[i].Error = "folder membership not confirmed by server"
			return result, errors.New(result.Peers[i].Error)
		}
	}
	for i, p := range m.Peers {
		row := &result.Peers[i]
		// A reply may arrive while another peer is being organized.
		dialog, err := source.organizationDialog(ctx, users[p.UserID])
		if err == nil {
			err = organizationHeadCheck(dialog, p)
		}
		if err != nil {
			row.Error = err.Error()
			return result, err
		}
		if dialog.FolderID != 1 {
			if err := source.organizationArchive(ctx, users[p.UserID]); err != nil {
				row.Error = "archive not confirmed; inspect before retry: " + err.Error()
				return result, errors.New(row.Error)
			}
		}
		dialog, err = source.organizationDialog(ctx, users[p.UserID])
		if err == nil {
			err = organizationHeadCheck(dialog, p)
		}
		if err != nil {
			row.Error = err.Error()
			return result, err
		}
		row.Archived = dialog.FolderID == 1
		if !row.Archived {
			row.Error = "archive not confirmed by server"
			return result, errors.New(row.Error)
		}
		filters, err = source.organizationFilters(ctx)
		if err != nil {
			row.Error = "folder readback failed: " + err.Error()
			return result, err
		}
		row.InFolder = completedWorkMembership(filters, folder.ID, p.UserID)
		if err := verifyCompletedWorkFilter(filters, folder); err != nil {
			row.Error = err.Error()
			return result, err
		}
		if !row.InFolder {
			row.Error = "folder membership absent after archive"
			return result, errors.New(row.Error)
		}
		row.Verified = true
	}
	result.Complete = true
	return result, nil
}

func organizationHeadCheck(d *tg.Dialog, p AccountOrganizePeer) error {
	if d == nil {
		return fmt.Errorf("user:%d dialog missing", p.UserID)
	}
	peer, ok := d.Peer.(*tg.PeerUser)
	if !ok || peer.UserID != p.UserID || d.TopMessage != p.LastMessageID {
		return fmt.Errorf("user:%d last message changed or peer identity differs; review again", p.UserID)
	}
	if d.Pinned && d.FolderID != 1 {
		return fmt.Errorf("user:%d is pinned; automatic archive cannot preserve pin metadata", p.UserID)
	}
	return nil
}

func filterIdentity(f tg.DialogFilterClass) (int, string) {
	switch v := f.(type) {
	case *tg.DialogFilter:
		return v.ID, v.Title.Text
	case *tg.DialogFilterChatlist:
		return v.ID, v.Title.Text
	}
	return 0, ""
}

func containsUser(peers []tg.InputPeerClass, id int64) bool {
	for _, peer := range peers {
		if user, ok := peer.(*tg.InputPeerUser); ok && user.UserID == id {
			return true
		}
	}
	return false
}

func completedWorkMembership(filters []tg.DialogFilterClass, folderID int, userID int64) bool {
	for _, filter := range filters {
		f, ok := filter.(*tg.DialogFilter)
		if ok && f.ID == folderID && f.Title.Text == CompletedWorkFolder {
			return !containsUser(f.ExcludePeers, userID) && (containsUser(f.IncludePeers, userID) || containsUser(f.PinnedPeers, userID))
		}
	}
	return false
}

func verifyCompletedWorkFilter(filters []tg.DialogFilterClass, expected *tg.DialogFilter) error {
	for _, filter := range filters {
		actual, ok := filter.(*tg.DialogFilter)
		if !ok || actual.ID != expected.ID {
			continue
		}
		// Native TL encoding compares every field and peer order while treating
		// nil and empty vectors identically. Never log these access-hash bytes.
		wantCopy, gotCopy := *expected, *actual
		var want, got bin.Buffer
		if err := wantCopy.Encode(&want); err != nil {
			return fmt.Errorf("encode expected folder for verification: %w", err)
		}
		if err := gotCopy.Encode(&got); err != nil {
			return fmt.Errorf("encode server folder for verification: %w", err)
		}
		if bytes.Equal(want.Buf, got.Buf) {
			return nil
		}
		return fmt.Errorf("server folder differs from the expected preserved fields or peer order; inspect before retry")
	}
	return fmt.Errorf("expected ordinary folder absent from server readback")
}

func mergeCompletedWorkFolder(filters []tg.DialogFilterClass, peers []AccountOrganizePeer, users map[int64]*tg.InputPeerUser) (*tg.DialogFilter, bool, error) {
	var folder *tg.DialogFilter
	used := map[int]bool{0: true, 1: true}
	for _, filter := range filters {
		id, title := filterIdentity(filter)
		used[id] = true
		if title != CompletedWorkFolder {
			continue
		}
		f, ok := filter.(*tg.DialogFilter)
		if !ok || folder != nil || f.ID < 2 {
			return nil, false, fmt.Errorf("folder %q must be one unambiguous ordinary filter", CompletedWorkFolder)
		}
		copy := *f
		copy.IncludePeers = append([]tg.InputPeerClass(nil), f.IncludePeers...)
		folder = &copy
	}
	changed := folder == nil
	if folder == nil {
		id := 2
		for used[id] && id <= 255 {
			id++
		}
		if id > 255 {
			return nil, false, fmt.Errorf("no free dialog filter ID")
		}
		folder = &tg.DialogFilter{ID: id, Title: tg.TextWithEntities{Text: CompletedWorkFolder}}
	}
	for _, p := range peers {
		user := users[p.UserID]
		if user == nil || user.UserID != p.UserID {
			return nil, false, fmt.Errorf("exact user:%d is not resolved in this account", p.UserID)
		}
		if containsUser(folder.ExcludePeers, p.UserID) {
			return nil, false, fmt.Errorf("user:%d is explicitly excluded from the folder; preserve existing filter", p.UserID)
		}
		if !containsUser(folder.IncludePeers, p.UserID) && !containsUser(folder.PinnedPeers, p.UserID) {
			folder.IncludePeers = append(folder.IncludePeers, user)
			changed = true
		}
	}
	return folder, changed, nil
}

func (s *Session) organizationFilters(ctx context.Context) ([]tg.DialogFilterClass, error) {
	var filters *tg.MessagesDialogFilters
	err := s.performRPC(ctx, "get_dialog_filters", func(ctx context.Context) error {
		var err error
		filters, err = s.raw.MessagesGetDialogFilters(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return filters.Filters, nil
}

func (s *Session) organizationUsers(ctx context.Context, peers []AccountOrganizePeer) (map[int64]*tg.InputPeerUser, error) {
	if _, err := s.ListAllDialogs(ctx); err != nil {
		return nil, err
	}
	users := map[int64]*tg.InputPeerUser{}
	for _, p := range peers {
		target, ok := s.dialogCache["user:"+strconv.FormatInt(p.UserID, 10)]
		if !ok {
			return nil, fmt.Errorf("user:%d not found in account dialogs", p.UserID)
		}
		user, ok := target.InputPeer.(*tg.InputPeerUser)
		if !ok || user.UserID != p.UserID {
			return nil, fmt.Errorf("user:%d is not an exact user peer", p.UserID)
		}
		users[p.UserID] = user
	}
	return users, nil
}

func (s *Session) organizationDialog(ctx context.Context, user *tg.InputPeerUser) (*tg.Dialog, error) {
	var result *tg.MessagesPeerDialogs
	err := s.performRPC(ctx, "get_peer_dialog", func(ctx context.Context) error {
		var err error
		result, err = s.raw.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: user}})
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, entry := range result.Dialogs {
		if dialog, ok := entry.(*tg.Dialog); ok {
			if peer, ok := dialog.Peer.(*tg.PeerUser); ok && peer.UserID == user.UserID {
				return dialog, nil
			}
		}
	}
	return nil, fmt.Errorf("user:%d dialog not returned by server", user.UserID)
}

func (s *Session) organizationUpdateFilter(ctx context.Context, filter *tg.DialogFilter) error {
	return s.performRPC(ctx, "update_completed_work_filter", func(ctx context.Context) error {
		ok, err := s.raw.MessagesUpdateDialogFilter(ctx, &tg.MessagesUpdateDialogFilterRequest{ID: filter.ID, Filter: filter})
		if err == nil && !ok {
			return fmt.Errorf("server rejected folder update")
		}
		return err
	})
}

func (s *Session) organizationArchive(ctx context.Context, user *tg.InputPeerUser) error {
	return s.performRPC(ctx, "archive_completed_work_peer", func(ctx context.Context) error {
		_, err := s.raw.FoldersEditPeerFolders(ctx, []tg.InputFolderPeer{{Peer: user, FolderID: 1}})
		return err
	})
}
