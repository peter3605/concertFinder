import SwiftUI

/// The artist and genre picker, as sections for a `Form` or `List`.
///
/// Sections rather than a screen so onboarding can put it under its own
/// heading and Settings can push it as a page, both over the one PicksModel.
/// Every search and name here is Ticketmaster's, which is the point: this is
/// how an account with no Spotify library says what it wants to see.
struct PicksEditorSections: View {
    @Environment(PicksModel.self) private var picks

    var body: some View {
        @Bindable var picks = picks

        if let error = picks.error {
            Section {
                InfoBanner(kind: .error(error.userMessage))
                    .listRowInsets(EdgeInsets())
                    .listRowBackground(Color.clear)
            }
        }

        Section {
            TextField("Search for an artist", text: $picks.query)
                .textInputAutocapitalization(.words)
                .autocorrectionDisabled()
                .submitLabel(.search)
                .accessibilityIdentifier("picks-search")

            if !picks.query.trimmingCharacters(in: .whitespaces).isEmpty {
                if picks.isSearching && picks.results.isEmpty {
                    HStack { ProgressView(); Text("Searching…").foregroundStyle(.secondary) }
                } else if picks.results.isEmpty && picks.query.count >= 2 && !picks.isSearching {
                    Text("No artists found on Ticketmaster.").foregroundStyle(.secondary)
                } else {
                    ForEach(picks.results) { result in
                        SearchResultRow(result: result)
                    }
                }
            }
        } header: {
            Text("Artists")
        } footer: {
            Text(picks.artistsFull
                 ? "You've picked the most artists you can (\(picks.maxArtists)). Remove one to add another."
                 : "Search by name. Artists with shows coming up are listed first.")
        }

        if !picks.artists.isEmpty {
            Section("Your artists (\(picks.artists.count) of \(picks.maxArtists))") {
                ForEach(picks.artists) { pick in
                    HStack {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(pick.name)
                            if let genre = pick.genre, !genre.isEmpty {
                                Text(genre).font(.caption).foregroundStyle(.secondary)
                            }
                        }
                        Spacer()
                        Button {
                            Task { await picks.remove(pick) }
                        } label: {
                            Image(systemName: "minus.circle.fill")
                                .foregroundStyle(.red)
                        }
                        .buttonStyle(.plain)
                        .disabled(picks.busyIDs.contains(pick.attractionId))
                        .accessibilityLabel("Remove \(pick.name)")
                    }
                }
            }
        }

        Section {
            if picks.catalog.isEmpty {
                HStack { ProgressView(); Text("Loading genres…").foregroundStyle(.secondary) }
            } else {
                GenreGrid()
                    .listRowInsets(EdgeInsets(top: Metrics.tight, leading: Metrics.gutter,
                                              bottom: Metrics.tight, trailing: Metrics.gutter))
            }
        } header: {
            Text("Genres (\(picks.genres.count) of \(picks.maxGenres))")
        } footer: {
            Text("Upcoming shows near you in these genres, soonest first.")
        }
    }
}

private struct SearchResultRow: View {
    @Environment(PicksModel.self) private var picks
    let result: PickSearchResult

    var body: some View {
        let picked = picks.isPicked(result)
        let busy = picks.busyIDs.contains(result.attractionId)
        Button {
            Task { await picks.add(result) }
        } label: {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text(result.name).foregroundStyle(.primary)
                    Text(detail).font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
                if busy {
                    ProgressView()
                } else {
                    Image(systemName: picked ? "checkmark.circle.fill" : "plus.circle")
                        .foregroundStyle(picked ? Color.accentColor : .secondary)
                }
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(picked || busy || picks.artistsFull)
        .accessibilityLabel(picked ? "\(result.name), picked" : "Pick \(result.name)")
        .accessibilityIdentifier("pick-result")
    }

    private var detail: String {
        var parts: [String] = []
        if let genre = result.genre, !genre.isEmpty { parts.append(genre) }
        parts.append(result.upcoming == 1 ? "1 upcoming event" : "\(result.upcoming) upcoming events")
        return parts.joined(separator: " · ")
    }
}

/// Genre chips. A flowing layout rather than a list of toggles: there are a
/// dozen-odd top-level genres and they read as a set to choose from.
private struct GenreGrid: View {
    @Environment(PicksModel.self) private var picks

    var body: some View {
        FlowLayout(spacing: Metrics.tight) {
            ForEach(picks.catalog) { genre in
                let picked = picks.isPicked(genre)
                Button {
                    Task { await picks.toggle(genre) }
                } label: {
                    // An explicit one-line HStack rather than a Label: in a
                    // List row, under this layout's proposals, Label's title
                    // was squeezed to nothing and the chips showed only an
                    // icon in a tall capsule.
                    HStack(spacing: 4) {
                        Image(systemName: picked ? "checkmark" : "plus")
                            .imageScale(.small)
                        Text(genre.name)
                            .lineLimit(1)
                    }
                        .fixedSize()
                        .font(.subheadline)
                        .padding(.horizontal, 12)
                        .padding(.vertical, 6)
                        .background(picked ? Color.accentColor.opacity(0.18) : Color.secondary.opacity(0.12))
                        .foregroundStyle(picked ? Color.accentColor : .primary)
                        .clipShape(Capsule())
                }
                .buttonStyle(.plain)
                .disabled(picks.busyIDs.contains(genre.id) || (!picked && picks.genresFull))
                .accessibilityLabel(genre.name)
                .accessibilityValue(picked ? "Picked" : "Not picked")
                .accessibilityAddTraits(picked ? .isSelected : [])
            }
        }
    }
}

/// Lays children out left to right, wrapping to a new row when one is full.
struct FlowLayout: Layout {
    var spacing: CGFloat = 8

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? .infinity
        var x: CGFloat = 0, y: CGFloat = 0, rowHeight: CGFloat = 0, widest: CGFloat = 0
        for view in subviews {
            let size = view.sizeThatFits(.unspecified)
            if x > 0 && x + size.width > width {
                y += rowHeight + spacing
                x = 0
                rowHeight = 0
            }
            x += size.width + spacing
            rowHeight = max(rowHeight, size.height)
            widest = max(widest, x - spacing)
        }
        return CGSize(width: proposal.width ?? widest, height: y + rowHeight)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var x = bounds.minX, y = bounds.minY, rowHeight: CGFloat = 0
        for view in subviews {
            let size = view.sizeThatFits(.unspecified)
            if x > bounds.minX && x + size.width > bounds.maxX {
                y += rowHeight + spacing
                x = bounds.minX
                rowHeight = 0
            }
            view.place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(size))
            x += size.width + spacing
            rowHeight = max(rowHeight, size.height)
        }
    }
}

/// Settings' page for the picker: the same editor, pushed.
struct PicksView: View {
    @Environment(PicksModel.self) private var picks

    var body: some View {
        Form {
            PicksEditorSections()
        }
        .navigationTitle("Artists and genres")
        .navigationBarTitleDisplayMode(.inline)
        .task { await picks.load() }
    }
}
