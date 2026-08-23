import SwiftUI

/// One show — one card, one night out.
///
/// The per-act controls are the thing to get right: a festival card carries
/// several acts, each with its own dedup key, and a bookmark that saved "the
/// card" would save the wrong artist.
struct EventCard: View {
    let event: Event
    var onToggleSave: (Act) -> Void
    var onToggleSubscribe: (Act) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: Metrics.tight) {
            header
            Divider()
            ForEach(event.acts) { act in
                ActRow(
                    act: act,
                    onToggleSave: { onToggleSave(act) },
                    onToggleSubscribe: { onToggleSubscribe(act) }
                )
            }
        }
        .padding(Metrics.gutter)
        .background(Color.cardBackground)
        .clipShape(RoundedRectangle(cornerRadius: Metrics.cardRadius, style: .continuous))
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(event.venue)
                .font(.headline)
            Text(event.location)
                .font(.subheadline)
                .foregroundStyle(.secondary)
            // Time is shown for a single-act bill only. On a festival the
            // date is the *earliest* act's set time, so presenting it as the
            // show's time would be a claim the data does not support.
            Text(event.acts.count == 1
                 ? event.date.formatted(date: .abbreviated, time: .shortened)
                 : event.date.formatted(date: .abbreviated, time: .omitted))
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// One artist on the bill, with its own save and subscribe controls.
struct ActRow: View {
    let act: Act
    var onToggleSave: () -> Void
    var onToggleSubscribe: () -> Void

    var body: some View {
        HStack(spacing: Metrics.tight) {
            VStack(alignment: .leading, spacing: 2) {
                Text(act.artist.name)
                    .font(.subheadline.weight(.medium))
                if let genres = act.artist.genres, !genres.isEmpty {
                    Text(genres.prefix(2).joined(separator: " · "))
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
            }
            Spacer(minLength: 0)

            Button(action: onToggleSubscribe) {
                Image(systemName: act.isSubscribed ? "bell.fill" : "bell")
            }
            .buttonStyle(.plain)
            .foregroundStyle(act.isSubscribed ? Color.accentColor : Color.secondary)
            .accessibilityLabel(act.isSubscribed
                                ? "Stop alerts for \(act.artist.name)"
                                : "Get alerts for \(act.artist.name)")

            Button(action: onToggleSave) {
                Image(systemName: act.isSaved ? "bookmark.fill" : "bookmark")
            }
            .buttonStyle(.plain)
            .foregroundStyle(act.isSaved ? Color.accentColor : Color.secondary)
            .accessibilityLabel(act.isSaved
                                ? "Remove \(act.artist.name) from saved"
                                : "Save \(act.artist.name)")
        }
        // Without this the two icon buttons swallow taps meant for the row's
        // navigation link.
        .contentShape(Rectangle())
    }
}
