import SwiftUI

/// Genre and venue pills with counts, a date range, and weekday/weekend.
///
/// Two rules the server depends on:
///
/// - Facet values go back **verbatim**. Genre matching is exact-tag
///   case-insensitive and venue matching runs under the server's own
///   normalizer, so "helpfully" lowercasing or trimming a value here turns a
///   pill that promises 12 results into one that returns none.
/// - A facet's count is what clicking it returns. The counts shown are the
///   server's, unmodified.
struct FiltersSheet: View {
    @Binding var filters: Filters
    let facets: Facets

    @Environment(\.dismiss) private var dismiss
    @State private var draft: Filters = .empty
    @State private var useDateRange = false
    @State private var fromDate = Date()
    @State private var toDate = Date().addingTimeInterval(60 * 60 * 24 * 90)

    var body: some View {
        NavigationStack {
            Form {
                weekdaySection
                dateSection
                facetSection(title: "Genre", facets: facets.genres, selection: $draft.genre)
                facetSection(title: "Venue", facets: facets.venues, selection: $draft.venue)
            }
            .navigationTitle("Filters")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button("Clear") { draft = .empty; useDateRange = false }
                        .disabled(draft.isEmpty)
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Apply") {
                        applyDates()
                        filters = draft
                        dismiss()
                    }
                    .fontWeight(.semibold)
                }
            }
            .onAppear {
                draft = filters
                useDateRange = !filters.dateFrom.isEmpty || !filters.dateTo.isEmpty
                // The pickers too, not just the toggle. Restoring the switch
                // alone reopened the sheet showing today-to-90-days over an
                // active filter, so re-applying anything else silently
                // widened the date range the user had set.
                if let from = Self.isoDay.date(from: filters.dateFrom) { fromDate = from }
                if let to = Self.isoDay.date(from: filters.dateTo) { toDate = to }
            }
        }
        .presentationDetents([.medium, .large])
    }

    private var weekdaySection: some View {
        Section("When") {
            Picker("Days", selection: $draft.weekday) {
                ForEach(Weekday.allCases, id: \.self) { day in
                    Text(day.label).tag(day)
                }
            }
            .pickerStyle(.segmented)
        }
    }

    private var dateSection: some View {
        Section {
            Toggle("Date range", isOn: $useDateRange)
            if useDateRange {
                DatePicker("From", selection: $fromDate, displayedComponents: .date)
                DatePicker("To", selection: $toDate, in: fromDate..., displayedComponents: .date)
            }
        }
    }

    @ViewBuilder
    private func facetSection(title: String, facets: [Facet], selection: Binding<String>) -> some View {
        if !facets.isEmpty {
            Section(title) {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: Metrics.tight) {
                        ForEach(facets) { facet in
                            Button {
                                // Toggle: tapping the selected pill clears it.
                                selection.wrappedValue =
                                    selection.wrappedValue == facet.value ? "" : facet.value
                            } label: {
                                Pill(
                                    text: facet.value,
                                    count: facet.count,
                                    isSelected: selection.wrappedValue == facet.value
                                )
                            }
                            .buttonStyle(.plain)
                        }
                    }
                    .padding(.vertical, 4)
                }
            }
        }
    }

    /// The API takes ISO dates. Formatted here rather than in the model so
    /// the model's Filters stays a plain value type.
    private func applyDates() {
        guard useDateRange else {
            draft.dateFrom = ""
            draft.dateTo = ""
            return
        }
        draft.dateFrom = Self.isoDay.string(from: fromDate)
        draft.dateTo = Self.isoDay.string(from: toDate)
    }

    private static let isoDay: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "yyyy-MM-dd"
        // Fixed locale: a user on a non-Gregorian calendar would otherwise
        // send dates the server cannot parse.
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = TimeZone(identifier: "UTC")
        return f
    }()
}
