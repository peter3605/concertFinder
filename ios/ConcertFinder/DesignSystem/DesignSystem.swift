import SwiftUI

/// Spacing, radii and type. Centralised so screens added later inherit the
/// same rhythm rather than each inventing padding.
enum Metrics {
    static let gutter: CGFloat = 16
    static let tight: CGFloat = 8
    static let loose: CGFloat = 24
    static let cardRadius: CGFloat = 14
    static let cardSpacing: CGFloat = 12
}

extension Color {
    /// Semantic colours. Defined against system materials so dark mode and
    /// increased-contrast settings are inherited rather than reimplemented.
    static let cardBackground = Color(.secondarySystemGroupedBackground)
    static let screenBackground = Color(.systemGroupedBackground)
    static let accentGreen = Color(red: 0.11, green: 0.73, blue: 0.33)
}

/// "Powered by Spotify" attribution.
///
/// Required on any surface showing Spotify-derived data — the feed, event
/// cards, the artists screen, the affinity view. It lives here as one
/// component precisely so a screen added later cannot forget it (plan §6).
///
/// The wordmark is text rather than a bundled logo asset: shipping Spotify's
/// mark means following their brand guidelines on size, clear space and
/// colour, and a wrong asset is a worse compliance story than an honest
/// typographic credit. Swap in the official asset before submission.
struct SpotifyAttribution: View {
    var body: some View {
        HStack(spacing: 6) {
            Image(systemName: "music.note")
                .font(.caption2)
            Text("Powered by Spotify")
                .font(.caption2)
        }
        .foregroundStyle(.secondary)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("Powered by Spotify")
    }
}

/// A labelled pill, used for genres and facet values.
struct Pill: View {
    let text: String
    var count: Int?
    var isSelected: Bool = false

    var body: some View {
        HStack(spacing: 4) {
            Text(text)
            if let count {
                Text("\(count)")
                    .foregroundStyle(isSelected ? .primary : .secondary)
                    .monospacedDigit()
            }
        }
        .font(.footnote)
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
        .background(isSelected ? Color.accentColor.opacity(0.18) : Color(.tertiarySystemFill))
        .foregroundStyle(isSelected ? Color.accentColor : Color.primary)
        .clipShape(Capsule())
    }
}

/// A banner for a state the user needs to understand, not an error.
///
/// `complete: false` and `retryAfter` are the two that matter: both mean the
/// list is thinner than it should be, and for different reasons the user can
/// act on differently.
struct InfoBanner: View {
    enum Kind {
        case incomplete
        case quotaExhausted(until: Date?)
        case offline(since: Date?)

        var icon: String {
            switch self {
            case .incomplete: "exclamationmark.triangle"
            case .quotaExhausted: "clock"
            case .offline: "wifi.slash"
            }
        }

        var title: String {
            switch self {
            case .incomplete: "Partial results"
            case .quotaExhausted: "Daily limit reached"
            case .offline: "Offline"
            }
        }

        var message: String {
            switch self {
            case .incomplete:
                // The distinction this flag exists to preserve: a quiet week
                // and a truncated scan look identical without saying so.
                "We couldn't check every artist in your profile this time, so there may be more shows than you see here."
            case .quotaExhausted(let until):
                if let until {
                    "We've used today's search allowance. New results after \(until.formatted(date: .omitted, time: .shortened))."
                } else {
                    "We've used today's search allowance. Check back tomorrow."
                }
            case .offline(let since):
                if let since {
                    "Showing concerts from \(since.relativeDescription)."
                } else {
                    "Showing the last concerts we loaded."
                }
            }
        }
    }

    let kind: Kind

    var body: some View {
        HStack(alignment: .top, spacing: Metrics.tight) {
            Image(systemName: kind.icon)
                .foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 2) {
                Text(kind.title).font(.footnote.weight(.semibold))
                Text(kind.message).font(.caption).foregroundStyle(.secondary)
            }
            Spacer(minLength: 0)
        }
        .padding(Metrics.gutter)
        .background(Color(.tertiarySystemFill))
        .clipShape(RoundedRectangle(cornerRadius: Metrics.cardRadius, style: .continuous))
        .accessibilityElement(children: .combine)
    }
}

extension Date {
    /// "3h ago", for the snapshot's computed_at.
    var relativeDescription: String {
        let formatter = RelativeDateTimeFormatter()
        formatter.unitsStyle = .abbreviated
        return formatter.localizedString(for: self, relativeTo: Date())
    }

    /// Month heading for the feed's sections.
    var monthHeading: String {
        formatted(.dateTime.month(.wide).year())
    }

    /// The key events are grouped into sections by.
    var monthKey: String {
        formatted(.dateTime.year().month(.twoDigits))
    }
}
