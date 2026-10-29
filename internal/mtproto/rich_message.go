package mtproto

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"
)

// Rich messages arrive outside Message.Message. Resolve truncated previews
// before committing history; an unsupported block must not become empty text.
func (s *Session) prepareRichMessage(ctx context.Context, target resolvedTarget, message tg.MessageClass) error {
	msg, ok := message.(*tg.Message)
	if !ok {
		return nil
	}
	rich, ok := msg.GetRichMessage()
	if !ok {
		return nil
	}
	if rich.Part {
		var full *tg.RichMessage
		err := s.performRPC(ctx, "get_rich_message", func(callCtx context.Context) error {
			result, err := s.raw.MessagesGetRichMessage(callCtx, &tg.MessagesGetRichMessageRequest{Peer: target.InputPeer, ID: msg.ID})
			if err != nil {
				return err
			}
			for _, candidate := range historyMessages(result) {
				if candidate, ok := candidate.(*tg.Message); ok && candidate.ID == msg.ID {
					if value, present := candidate.GetRichMessage(); present {
						full = &value
					}
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("rich message %d: %w", msg.ID, err)
		}
		if full == nil || full.Part {
			return fmt.Errorf("rich message %d remains partial", msg.ID)
		}
		msg.SetRichMessage(*full)
		rich = *full
	}
	_, _, err := richMessageContent(rich)
	if err != nil {
		return fmt.Errorf("rich message %d: %w", msg.ID, err)
	}
	return nil
}

func richMessageContent(rich tg.RichMessage) (string, []string, error) {
	if rich.Part {
		return "", nil, fmt.Errorf("partial rich message needs resolution")
	}
	if _, err := richMessageMedia(rich); err != nil {
		return "", nil, err
	}
	photos, documents := map[int64]bool{}, map[int64]bool{}
	for _, photo := range rich.Photos {
		photos[photo.(*tg.Photo).ID] = true
	}
	for _, document := range rich.Documents {
		documents[document.(*tg.Document).ID] = true
	}
	var links []string
	var text func(tg.RichTextClass) (string, error)
	text = func(value tg.RichTextClass) (string, error) {
		if value == nil {
			return "", nil
		}
		if link, ok := value.(interface{ GetURL() string }); ok {
			links = mergeLinks(links, []string{link.GetURL()})
		}
		switch v := value.(type) {
		case *tg.TextEmpty:
			return "", nil
		case *tg.TextCustomEmoji:
			if v.Alt != "" {
				return v.Alt, nil
			}
			return fmt.Sprintf("[custom emoji %d]", v.DocumentID), nil
		case *tg.TextConcat:
			var out strings.Builder
			for _, child := range v.Texts {
				part, err := text(child)
				if err != nil {
					return "", err
				}
				out.WriteString(part)
			}
			return out.String(), nil
		case interface{ GetText() string }:
			return v.GetText(), nil
		case interface{ GetText() tg.RichTextClass }:
			return text(v.GetText())
		default:
			return "", fmt.Errorf("unsupported rich text %T", value)
		}
	}
	var blocks func([]tg.PageBlockClass) (string, error)
	blocks = func(values []tg.PageBlockClass) (string, error) {
		var out []string
		for _, block := range values {
			var part string
			var err error
			switch v := block.(type) {
			case *tg.PageBlockCover:
				part, err = blocks([]tg.PageBlockClass{v.Cover})
			case *tg.PageBlockCollage:
				part, err = blocks(v.Items)
			case *tg.PageBlockSlideshow:
				part, err = blocks(v.Items)
			case *tg.PageBlockPhoto:
				if !photos[v.PhotoID] {
					err = fmt.Errorf("rich photo %d is missing", v.PhotoID)
				}
				if v.URL != "" {
					links = mergeLinks(links, []string{v.URL})
				}
			case *tg.PageBlockDocument:
				if !documents[v.DocumentID] {
					err = fmt.Errorf("rich document %d is missing", v.DocumentID)
				}
			case *tg.PageBlockVideo:
				if !documents[v.VideoID] {
					err = fmt.Errorf("rich video %d is missing", v.VideoID)
				}
			case *tg.PageBlockAudio:
				if !documents[v.AudioID] {
					err = fmt.Errorf("rich audio %d is missing", v.AudioID)
				}
			case *tg.PageBlockDivider:
				continue
			case *tg.PageBlockTable:
				part, err = richTableText(v, text)
			case *tg.PageBlockList:
				var list []string
				for _, item := range v.Items {
					switch item := item.(type) {
					case *tg.PageListItemText:
						part, err = text(item.Text)
						prefix := "- "
						if item.Checkbox {
							prefix = "- [ ] "
							if item.Checked {
								prefix = "- [x] "
							}
						}
						part = prefix + part
					case *tg.PageListItemBlocks:
						part, err = blocks(item.Blocks)
						part = "- " + part
					default:
						err = fmt.Errorf("unsupported rich list item %T", item)
					}
					if err != nil {
						return "", err
					}
					list = append(list, part)
				}
				part = strings.Join(list, "\n")
			case *tg.PageBlockOrderedList:
				var list []string
				for i, item := range v.Items {
					prefix := strconv.Itoa(i + 1)
					switch item := item.(type) {
					case *tg.PageListOrderedItemText:
						if item.Num != "" {
							prefix = item.Num
						}
						part, err = text(item.Text)
					case *tg.PageListOrderedItemBlocks:
						if item.Num != "" {
							prefix = item.Num
						}
						part, err = blocks(item.Blocks)
					default:
						err = fmt.Errorf("unsupported rich ordered item %T", item)
					}
					if err != nil {
						return "", err
					}
					list = append(list, prefix+". "+part)
				}
				part = strings.Join(list, "\n")
			case *tg.PageBlockDetails:
				part, err = text(v.Title)
				if err == nil {
					var body string
					body, err = blocks(v.Blocks)
					part += "\n\n" + body
				}
			case *tg.PageBlockBlockquoteBlocks:
				part, err = blocks(v.Blocks)
				if err == nil {
					var caption string
					caption, err = text(v.Caption)
					if caption != "" {
						part += "\n" + caption
					}
				}
			case *tg.PageBlockPreformatted:
				part, err = text(v.Text)
				part = "```" + v.Language + "\n" + part + "\n```"
			case interface{ GetText() tg.RichTextClass }:
				part, err = text(v.GetText())
				if caption, ok := block.(interface{ GetCaption() tg.RichTextClass }); ok && err == nil {
					var extra string
					extra, err = text(caption.GetCaption())
					if extra != "" {
						part += "\n" + extra
					}
				}
			default:
				err = fmt.Errorf("unsupported rich block %T", block)
			}
			if caption, ok := block.(interface{ GetCaption() tg.PageCaption }); ok && err == nil {
				value := caption.GetCaption()
				for _, richText := range []tg.RichTextClass{value.Text, value.Credit} {
					var extra string
					extra, err = text(richText)
					if err != nil {
						break
					}
					if extra != "" {
						part = strings.TrimSpace(part + "\n" + extra)
					}
				}
			}
			if err != nil {
				return "", err
			}
			if part != "" {
				out = append(out, part)
			}
		}
		return strings.Join(out, "\n\n"), nil
	}
	body, err := blocks(rich.Blocks)
	return strings.TrimSpace(body), mergeLinks(links, extractLinks(body, nil)), err
}

// Plain text has no merged cells. Keep source row/cell order and annotate spans
// rather than inventing a rectangular grid or dropping empty cells.
func richTableText(table *tg.PageBlockTable, text func(tg.RichTextClass) (string, error)) (string, error) {
	title, err := text(table.Title)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	out.WriteString("Table")
	if title != "" {
		out.WriteString(": " + title)
	}
	for rowIndex, row := range table.Rows {
		fmt.Fprintf(&out, "\nRow %d:", rowIndex+1)
		for cellIndex, cell := range row.Cells {
			var attributes []string
			if cell.Header {
				attributes = append(attributes, "header")
			}
			if span, ok := cell.GetColspan(); ok {
				attributes = append(attributes, "colspan="+strconv.Itoa(span))
			}
			if span, ok := cell.GetRowspan(); ok {
				attributes = append(attributes, "rowspan="+strconv.Itoa(span))
			}
			value, _ := cell.GetText()
			content, err := text(value)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&out, "\n- Cell %d", cellIndex+1)
			if len(attributes) != 0 {
				fmt.Fprintf(&out, " [%s]", strings.Join(attributes, ", "))
			}
			out.WriteString(": " + content)
		}
	}
	return out.String(), nil
}

// Reuse the ordinary media path, including download limits, cache and ASR.
// Container resources stay in their source order even when a block has no caption.
func richMessageMedia(rich tg.RichMessage) ([]tg.MessageMediaClass, error) {
	var media []tg.MessageMediaClass
	for _, value := range rich.Photos {
		photo, ok := value.(*tg.Photo)
		if !ok || photo.ID == 0 {
			return nil, fmt.Errorf("rich message contains an unavailable photo")
		}
		item := &tg.MessageMediaPhoto{}
		item.SetPhoto(photo)
		media = append(media, item)
	}
	for _, value := range rich.Documents {
		document, ok := value.(*tg.Document)
		if !ok || document.ID == 0 {
			return nil, fmt.Errorf("rich message contains an unavailable document")
		}
		item := &tg.MessageMediaDocument{}
		item.SetDocument(document)
		media = append(media, item)
	}
	return media, nil
}
