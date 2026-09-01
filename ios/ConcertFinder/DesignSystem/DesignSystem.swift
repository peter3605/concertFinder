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
/// cards, the artists screen. It lives here as one component precisely so a
/// screen added later cannot forget it (plan §6).
///
/// ## The mark is Spotify's asset, and every number here comes from their guidelines
///
/// The wordmark may not be recreated, so `SpotifyLogo` in the asset catalogue
/// is the official file from Spotify's brand resource kit, unmodified. Four
/// rules shape what is below, and none of them is a matter of taste:
///
/// - **Full logo, not the icon.** Partner integrations use the icon+wordmark
///   lockup; the bare icon is only allowed when it stands in as an app icon on
///   a device's home screen, which is not this.
/// - **Minimum 70pt wide** for the full logo (the icon alone has a separate,
///   smaller 21pt floor that does not apply here). The old placeholder was
///   16pt, under even that.
/// - **Clear space of half the mark's height** on every side, to keep it away
///   from competing elements.
/// - **Colourway.** Spotify green is restricted to black or white
///   backgrounds. Attribution sits on `systemGroupedBackground`, which is
///   neither in either appearance, so the monochrome black and white variants
///   are the compliant choice — carried by the asset catalogue's light/dark
///   appearance slots rather than by tinting one file.
///
/// That last point is why `.foregroundStyle(.secondary)` applies to the label
/// only. Applying it to the image would recolour the mark to a translucent
/// grey, which is a modification of the logo and not one of the approved
/// colourways — the muted look has to come from choosing the right asset, not
/// from an opacity.
///
/// The label reads "Powered by" rather than "Powered by Spotify" because the
/// full logo already contains the wordmark; the old text plus this asset would
/// say Spotify twice.
struct SpotifyAttribution: View {
    /// Spotify's stated floor for the full logo. Scaled with the text so it
    /// holds at accessibility sizes rather than being dwarfed by the label —
    /// scaling up is fine, and the `@ScaledMetric` base is the minimum, so it
    /// never scales below it.
    @ScaledMetric(relativeTo: .caption2) private var logoWidth: CGFloat = 70

    /// From the asset's own viewBox (823.46 × 225.25). Hard-coding the ratio
    /// keeps the mark from being stretched if the frame is ever given both
    /// dimensions.
    private static let logoAspectRatio: CGFloat = 823.46 / 225.25

    private var logoHeight: CGFloat { logoWidth / Self.logoAspectRatio }

    /// Half the mark's height, per the guidelines' exclusion zone.
    private var clearSpace: CGFloat { logoHeight / 2 }

    var body: some View {
        HStack(spacing: clearSpace) {
            Text("Powered by")
                .font(.caption2)
                .foregroundStyle(.secondary)
            Image("SpotifyLogo")
                .resizable()
                // .original, not .template: a template render would take the
                // foreground colour and defeat the point of shipping the two
                // approved colourways.
                .renderingMode(.original)
                .scaledToFit()
                .frame(width: logoWidth, height: logoHeight)
        }
        .padding(clearSpace)
        // One element: the mark and the label say the same thing together, so
        // VoiceOver should read the phrase once rather than announcing an
        // image beside it.
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
        /// A request failed. The models all set `error` and, until this
        /// existed, only one screen rendered any of it.
        case error(String)
        /// A manual rescan was refused. `reason` is the server's own wording,
        /// which is the half that says whether the wait is fifteen minutes or
        /// the rest of the UTC day.
        case throttled(reason: String?, until: Date?)
        /// A notification's event key matched nothing in the feed.
        case missingEvent

        var icon: String {
            switch self {
            case .incomplete: return "exclamationmark.triangle"
            case .quotaExhausted: return "clock"
            case .offline: return "wifi.slash"
            case .error: return "exclamationmark.triangle"
            case .throttled: return "clock"
            case .missingEvent: return "questionmark.circle"
            }
        }

        var title: String {
            switch self {
            case .incomplete: return "Partial results"
            case .quotaExhausted: return "Daily limit reached"
            case .offline: return "Offline"
            case .error: return "Couldn't refresh"
            case .throttled: return "Not just yet"
            case .missingEvent: return "Show not in your feed"
            }
        }

        var message: String {
            switch self {
            case .incomplete:
                // The distinction this flag exists to preserve: a quiet week
                // and a truncated scan look identical without saying so.
                return "We couldn't check every artist in your profile this time, so there may be more shows than you see here."
            case .quotaExhausted(let until):
                if let until {
                    return "We've used today's search allowance. New results after \(until.formatted(date: .omitted, time: .shortened))."
                }
                return "We've used today's search allowance. Check back tomorrow."
            case .offline(let since):
                if let since {
                    return "Showing concerts from \(since.relativeDescription)."
                }
                return "Showing the last concerts we loaded."
            case .error(let message):
                return message
            case .throttled(let reason, let until):
                let lead = reason ?? "We checked for you a moment ago."
                if let until {
                    return "\(lead) You can search again after \(until.formatted(date: .omitted, time: .shortened))."
                }
                return "\(lead) Try again in a few minutes."
            case .missingEvent:
                // Both causes, because the app cannot tell them apart and
                // guessing one would be wrong half the time.
                return "That show isn't in your feed right now — it may have passed, or your filters may be hiding it."
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

    /// The key events are grouped into sections by. Sorts chronologically as
    /// text: "2026-08" < "2026-09" < "2027-01".
    ///
    /// Built from calendar components rather than `.formatted`, because
    /// `.formatted` arranges fields by *locale* and not by the order they are
    /// listed. `.dateTime.year().month(.twoDigits)` renders "08/2026" on a US
    /// device, so sorting the keys as text put "01/2027" above "08/2026" and
    /// stood the feed's month sections on their head -- next year first.
    ///
    /// `Calendar.current` keeps the bucketing *local*, which the cards depend
    /// on: a show at 00:30Z on Sep 1 renders as "Aug 31" in an eastern
    /// timezone and has to sit under August with it.
    var monthKey: String {
        let parts = Calendar.current.dateComponents([.year, .month], from: self)
        return String(format: "%04d-%02d", parts.year ?? 0, parts.month ?? 0)
    }
}
