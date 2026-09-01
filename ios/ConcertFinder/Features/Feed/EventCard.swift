import SwiftUI

/// One show — one card, one night out.
///
/// The per-act controls are the thing to get right: a festival card carries
/// several acts, each with its own dedup key, and a bookmark that saved "the
/// card" would save the wrong artist.
struct EventCard: View {
    let event: Event
    /// Both nil on the signed-out "popular shows near you" backdrop. Those
    /// acts come from `/api/discover`, which carries no artist id and no
    /// saved/subscribed state because it does not know who is asking — so
    /// the controls are *absent* there rather than present and inert.
    var onToggleSave: ((Act) -> Void)?
    var onToggleSubscribe: ((Act) -> Void)?

    var body: some View {
        VStack(alignment: .leading, spacing: Metrics.tight) {
            header
            Divider()
            ForEach(event.acts) { act in
                ActRow(
                    act: act,
                    onToggleSave: saveAction(for: act),
                    onToggleSubscribe: subscribeAction(for: act)
                )
            }
        }
        .padding(Metrics.gutter)
        .background(Color.cardBackground)
        .clipShape(RoundedRectangle(cornerRadius: Metrics.cardRadius, style: .continuous))
    }

    /// Absent callback in, absent control out — the per-act closures are what
    /// carry "there is somebody to do this on behalf of".
    private func saveAction(for act: Act) -> (() -> Void)? {
        guard let onToggleSave else { return nil }
        return { onToggleSave(act) }
    }

    private func subscribeAction(for act: Act) -> (() -> Void)? {
        guard let onToggleSubscribe else { return nil }
        return { onToggleSubscribe(act) }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(event.venue)
                .font(.headline)
            // The promoter's title for the night, when it is not just the
            // act's name repeated. This is the line that tells you the show
            // is a festival rather than a club date.
            if let name = event.name, !name.isEmpty {
                Text(name)
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }
            Text(event.location)
                .font(.subheadline)
                .foregroundStyle(.secondary)
            // Time is shown for a single-act bill only. On a festival the
            // date is the *earliest* act's set time, so presenting it as the
            // show's time would be a claim the data does not support.
            HStack(spacing: Metrics.tight) {
                Text(event.acts.count == 1
                     ? event.date.formatted(date: .abbreviated, time: .shortened)
                     : event.date.formatted(date: .abbreviated, time: .omitted))
                    .font(.caption)
                    .foregroundStyle(.secondary)
                if event.isFestival == true {
                    Text("Festival")
                        .font(.caption2.weight(.semibold))
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(Color(.tertiarySystemFill))
                        .clipShape(Capsule())
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        // Three separate labels read as three swipes for one heading.
        // Combined, VoiceOver announces the card once, the way a sighted
        // user reads it.
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isHeader)
    }
}

/// Where an act sits on the bill, when there is any basis for saying.
///
/// An unknown slot renders nothing rather than a neutral label. It is
/// genuinely unknown for every event that did not come from Ticketmaster, and
/// silence is honest where "Support" would be a claim. The wording stays soft
/// for the same reason: the ordering behind it is inferred, not published.
struct BillingLabel: View {
    let slot: Act.Billing?

    var body: some View {
        if let slot {
            Text(slot == .headliner ? "Headlining" : "Support")
                .font(.caption2)
                .foregroundStyle(.secondary)
                .padding(.horizontal, 6)
                .padding(.vertical, 2)
                .background(Color(.tertiarySystemFill))
                .clipShape(Capsule())
        }
    }
}

/// One artist on the bill, with its own save and subscribe controls.
struct ActRow: View {
    let act: Act
    /// Nil where there is nobody to act on behalf of — see `EventCard`.
    var onToggleSave: (() -> Void)?
    var onToggleSubscribe: (() -> Void)?

    var body: some View {
        HStack(spacing: Metrics.tight) {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(act.artist.name)
                        .font(.subheadline.weight(.medium))
                    BillingLabel(slot: act.billingSlot)
                }
                // Why this artist is here at all. This is the product's whole
                // differentiator -- we read your listening and scored these
                // artists -- and until it was on the card there was nothing
                // separating the feed from a generic local-listings site.
                //
                // It takes the genres' line rather than adding a third: on a
                // six-act festival card an extra caption per act is most of a
                // screen, and the reason is the more useful of the two. An
                // absent reason (an older profile, an artist we have nothing
                // honest to say about, the signed-out backdrop) falls back to
                // the genres exactly as before.
                if let reason = act.reason, !reason.isEmpty {
                    Text(reason)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                } else if let genres = act.artist.genres, !genres.isEmpty {
                    Text(genres.prefix(2).joined(separator: " · "))
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
            }
            .accessibilityElement(children: .combine)
            Spacer(minLength: 0)

            // Both controls carry the artist's name, because on a festival
            // card there are six identical bell icons and "Get alerts" alone
            // would not say which act it acts on. The label states the action
            // and the toggle state carries the current value, so VoiceOver
            // does not read a stale "on/off" alongside a changing verb.
            if let onToggleSubscribe {
                Button(action: onToggleSubscribe) {
                    Image(systemName: act.isSubscribed ? "bell.fill" : "bell")
                }
                .buttonStyle(.plain)
                .foregroundStyle(act.isSubscribed ? Color.accentColor : Color.secondary)
                .accessibilityLabel("Alerts for \(act.artist.name)")
                .accessibilityValue(act.isSubscribed ? "On" : "Off")
                .accessibilityAddTraits(act.isSubscribed ? [.isButton, .isSelected] : .isButton)
                .accessibilityHint(act.isSubscribed
                                   ? "Double tap to stop alerts when this artist announces a show"
                                   : "Double tap to get alerts when this artist announces a show")
            }

            if let onToggleSave {
                Button(action: onToggleSave) {
                    Image(systemName: act.isSaved ? "bookmark.fill" : "bookmark")
                }
                .buttonStyle(.plain)
                .foregroundStyle(act.isSaved ? Color.accentColor : Color.secondary)
                .accessibilityLabel("Save \(act.artist.name)")
                .accessibilityValue(act.isSaved ? "Saved" : "Not saved")
                .accessibilityAddTraits(act.isSaved ? [.isButton, .isSelected] : .isButton)
            }
        }
        // Without this the two icon buttons swallow taps meant for the row's
        // navigation link.
        .contentShape(Rectangle())
    }
}
