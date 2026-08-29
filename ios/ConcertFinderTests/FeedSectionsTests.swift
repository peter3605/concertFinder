import Foundation
import Testing

@testable import ConcertFinder

struct FeedSectionsTests {

    /// Noon local on the 15th: far enough from either edge of the month that
    /// the local-vs-UTC bucketing question cannot affect which month it is.
    private static func date(_ year: Int, _ month: Int, day: Int = 15) -> Date {
        Calendar.current.date(from: DateComponents(
            year: year, month: month, day: day, hour: 12
        ))!
    }

    private static func event(_ date: Date) -> Event {
        Event(
            eventKey: ISO8601DateFormatter().string(from: date),
            date: date,
            venue: "9:30 Club",
            city: "Washington",
            state: "DC",
            country: "US",
            latitude: nil,
            longitude: nil,
            acts: [],
            links: []
        )
    }

    /// The feed reads soonest-first. It shipped backwards: `monthKey` was
    /// `.formatted(.dateTime.year().month(.twoDigits))`, which renders
    /// "08/2026" on a US device because `.formatted` orders fields by locale,
    /// so sorting the keys as text put "01/2027" at the top of the list.
    @Test func sectionsRunSoonestFirstAcrossAYearBoundary() {
        let months = [(2027, 1), (2026, 8), (2027, 2), (2026, 12), (2026, 9)]
        let events = months.map { Self.event(Self.date($0.0, $0.1)) }

        let sections = FeedModel.monthSections(for: events)

        #expect(sections.map(\.key) == ["2026-08", "2026-09", "2026-12", "2027-01", "2027-02"])
    }

    /// Within a section too, not just between them.
    @Test func eventsInsideASectionRunSoonestFirst() {
        let days = [28, 3, 17, 1]
        let events = days.map { Self.event(Self.date(2026, 9, day: $0)) }

        let sections = FeedModel.monthSections(for: events)

        #expect(sections.count == 1)
        #expect(sections[0].events.map { Calendar.current.component(.day, from: $0.date) }
            == [1, 3, 17, 28])
    }

    /// The key is a sort key, so it has to compare chronologically as text
    /// regardless of what the heading beside it reads like.
    @Test func monthKeysAreChronologicalAsStrings() {
        let keys = [(2026, 8), (2026, 9), (2026, 12), (2027, 1), (2027, 2)]
            .map { Self.date($0.0, $0.1).monthKey }
        #expect(keys == keys.sorted())
    }
}
