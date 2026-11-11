package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chupakobra6/telegram-harvest/internal/mtproto"
)

func TestDeliverySummaryKeepsPartialOutcomeAndFileEvidence(t *testing.T) {
	result := mtproto.PrintShopPublishResult{AccountID: 123, ChannelID: 456, Title: "reviewed group", Ready: true, Complete: false,
		Rows: []mtproto.PrintShopPublishRow{
			{Post: mtproto.PrintShopPost{Key: "first", Text: strings.Repeat("Проверенный текст. ", 150), File: &mtproto.PrintShopPDF{Path: "/private/a.pdf", Filename: "a.pdf", MIME: "application/pdf", Size: 99, SHA256: strings.Repeat("a", 64)}}, ID: 11, Status: "sent", ServerVerified: true},
			{Post: mtproto.PrintShopPost{Key: "second", Text: "Не повторять при неизвестном исходе"}, Status: "outcome_unknown", OutcomeUnknown: true},
		}}
	var full, compact bytes.Buffer
	if err := writeDeliveryResult(&full, result, false); err != nil {
		t.Fatal(err)
	}
	if err := writeDeliveryResult(&compact, result, true); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		AccountID      int64                         `json:"account_id"`
		ChannelID      int64                         `json:"channel_id"`
		Complete       bool                          `json:"complete"`
		ContentOmitted bool                          `json:"content_omitted"`
		Rows           []mtproto.PrintShopPublishRow `json:"rows"`
	}
	if err := json.Unmarshal(compact.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.AccountID != 123 || decoded.ChannelID != 456 || decoded.Complete || !decoded.ContentOmitted || len(decoded.Rows) != 2 {
		t.Fatalf("lost envelope: %+v", decoded)
	}
	first, second := decoded.Rows[0], decoded.Rows[1]
	if first.ID != 11 || !first.ServerVerified || first.Post.Key != "first" || first.Post.File.SHA256 != strings.Repeat("a", 64) || first.Post.File.Size != 99 {
		t.Fatalf("lost confirmed evidence: %+v", first)
	}
	if second.Status != "outcome_unknown" || !second.OutcomeUnknown || second.ServerVerified {
		t.Fatalf("lost unknown outcome: %+v", second)
	}
	if first.Post.Text != "" || bytes.Contains(compact.Bytes(), []byte("Проверенный текст")) {
		t.Fatal("summary repeated body")
	}
	if !bytes.Contains(full.Bytes(), []byte("Проверенный текст")) {
		t.Fatal("review preview lost body")
	}
	if compact.Len()*2 >= full.Len() {
		t.Fatalf("summary not meaningfully smaller: full=%d summary=%d", full.Len(), compact.Len())
	}
	t.Logf("synthetic checked receipt: full=%d bytes summary=%d bytes", full.Len(), compact.Len())
}

func TestDirectReplySummaryKeepsServerVerification(t *testing.T) {
	r := mtproto.DirectReplyResult{AccountID: 123, PeerID: 456, ReplyTo: 10, MessageID: 11, Text: "private outgoing", IncomingText: "private incoming", Status: "already_sent", ServerVerified: true}
	var out bytes.Buffer
	if err := writeDeliveryResult(&out, r, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "private ") {
		t.Fatal("receipt repeats full exchange")
	}
	var decoded mtproto.DirectReplyResult
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.MessageID != 11 || decoded.ReplyTo != 10 || !decoded.ServerVerified || decoded.Status != "already_sent" {
		t.Fatalf("lost result: %+v", decoded)
	}
}
