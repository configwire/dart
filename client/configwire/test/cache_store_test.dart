import 'dart:convert';

import 'package:configwire/configwire.dart';
import 'package:test/test.dart';

/// CacheData model suite: parsing, tolerance, and round-trip.
///
/// Persistence lives behind the CacheStore seam; implement your own
/// store for disk.
void main() {
  CacheData sample() => CacheData(
        etag: 'etag-abc',
        version: 3,
        fetchedAt: DateTime.utc(2026, 9, 22, 12, 34, 56),
        values: {'flag_bool': true, 'flag_int': 7},
        variants: {'flag_bool': 'treated', 'anon_flag': ''},
      );

  group('CacheData model', () {
    test('toJson/fromJson round-trips all keys', () {
      final data = sample();
      final decoded = CacheData.fromJson(
        Map<String, Object?>.from(jsonDecode(jsonEncode(data.toJson()))),
      );
      expect(decoded, isNotNull);
      expect(decoded!.etag, 'etag-abc');
      expect(decoded.version, 3);
      expect(
        decoded.fetchedAt.toUtc().toIso8601String(),
        DateTime.utc(2026, 9, 22, 12, 34, 56).toIso8601String(),
      );
      expect(decoded.values, {'flag_bool': true, 'flag_int': 7});
      expect(decoded.variants, {'flag_bool': 'treated', 'anon_flag': ''});
    });

    test('missing variants tolerates to {}', () {
      final decoded = CacheData.fromJson({
        'etag': 'e',
        'version': 1,
        'fetchedAt': '2026-09-22T00:00:00.000Z',
        'values': <String, Object?>{},
      });
      expect(decoded, isNotNull);
      expect(decoded!.variants, isEmpty);
    });

    test('non-string variant entries dropped, "" preserved', () {
      final decoded = CacheData.fromJson({
        'etag': 'e',
        'version': 1,
        'fetchedAt': '2026-09-22T00:00:00.000Z',
        'values': <String, Object?>{},
        'variants': {'ok': 'arm', 'anon': '', 'bad': 7, 'null': null, 8: 'x'},
      });
      expect(decoded, isNotNull);
      expect(decoded!.variants, {'ok': 'arm', 'anon': ''});
    });

    test('wrong shapes return null; num coerced; bad date is epoch', () {
      expect(CacheData.fromJson({'etag': 7, 'values': <String, Object?>{}}),
          isNull);
      expect(CacheData.fromJson({'etag': 'e', 'values': [1, 2]}), isNull);
      final coerced = CacheData.fromJson({
        'etag': 'e',
        'version': 2.9,
        'fetchedAt': 'not-a-date',
        'values': <String, Object?>{},
      });
      expect(coerced, isNotNull);
      expect(coerced!.version, 2);
      expect(
        coerced.fetchedAt.toUtc().millisecondsSinceEpoch,
        DateTime.fromMillisecondsSinceEpoch(0, isUtc: true)
            .millisecondsSinceEpoch,
      );
    });

    test('malformed strings return null without throwing', () {
      expect(cacheDataFromJsonString('{truncated'), isNull);
      expect(cacheDataFromJsonString('{{{not json'), isNull);
      expect(cacheDataFromJsonString('[1,2]'), isNull);
      expect(cacheDataFromJsonString('42'), isNull);
    });
  });
}
