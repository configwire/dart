@TestOn('vm')
library;
import 'dart:io';

import 'package:configwire/configwire.dart';
import 'package:hive_ce/hive_ce.dart';
import 'package:test/test.dart';

/// Core Hive store suite: `HiveCacheStore` over a host-opened box.
///
/// One shared temp dir (`setUpAll` + single `Hive.init`); every test opens
/// a UNIQUE box and closes + deletes it in `tearDown` so no test observes
/// another's persisted state. Adversarial probes: corrupt string,
/// wrong-type value, closed box, tenant-key isolation, `toString`
/// redaction.
CacheData _sample() => CacheData(
      etag: 'etag-abc',
      version: 7,
      fetchedAt: DateTime.utc(2026, 9, 22, 12, 34, 56),
      values: {
        'launch_flag': true,
        'limit': 42,
        'name': 'hello',
      },
      variants: {'launch_flag': 'control', 'empty_arm': ''},
    );

void main() {
  late Directory tmp;
  final opened = <String>[];

  setUpAll(() async {
    tmp = await Directory.systemTemp.createTemp('cw-core-hive-test-');
    Hive.init(tmp.path);
  });

  tearDownAll(() async {
    for (final name in opened) {
      try {
        if (Hive.isBoxOpen(name)) await Hive.box(name).close();
        await Hive.deleteBoxFromDisk(name);
      } catch (_) {
        // Best-effort cleanup; never throws the suite.
      }
    }
    opened.clear();
    if (await tmp.exists()) await tmp.delete(recursive: true);
  });

  var counter = 0;
  Future<Box> freshBox() async {
    final name = 'core_box_${counter++}';
    opened.add(name);
    return Hive.openBox(name);
  }

  group('HiveCacheStore', () {
    test('default cacheKey scheme is cache_<env>', () async {
      final box = await freshBox();
      final store = HiveCacheStore(box: box, env: 'dev');
      expect(store.cacheKey, 'cache_dev');
      final prodBox = await freshBox();
      expect(HiveCacheStore(box: prodBox, env: 'prod').cacheKey, 'cache_prod');
    });

    test('round-trip preserves etag/version/fetchedAt/values/variants',
        () async {
      final store = HiveCacheStore(box: await freshBox(), env: 'dev');
      final data = _sample();
      await store.save(data);
      final loaded = await store.load();
      expect(loaded, isNotNull);
      expect(loaded!.etag, data.etag);
      expect(loaded.version, data.version);
      expect(
        loaded.fetchedAt.toUtc().toIso8601String(),
        data.fetchedAt.toUtc().toIso8601String(),
      );
      expect(loaded.values, data.values);
      expect(loaded.variants, data.variants);
      // "" variant arm preserved at the store layer.
      expect(loaded.variants['empty_arm'], '');
    });

    test('missing key returns null without throwing', () async {
      final store = HiveCacheStore(box: await freshBox(), env: 'nope');
      expect(await store.load(), isNull);
    });

    test('corrupt box value returns null without throwing', () async {
      final box = await freshBox();
      await box.put('cache_dev', 'not-json{{{garbage');
      final store = HiveCacheStore(box: box, env: 'dev');
      expect(await store.load(), isNull);
    });

    test('wrong-type box value returns null without throwing', () async {
      final box = await freshBox();
      await box.put('cache_dev', 42);
      final store = HiveCacheStore(box: box, env: 'dev');
      expect(await store.load(), isNull);
    });

    test('closed box degrades to null/noop without throwing', () async {
      final box = await freshBox();
      final store = HiveCacheStore(box: box, env: 'dev');
      await store.save(_sample());
      expect(await store.load(), isNotNull);
      await box.close();
      expect(await store.load(), isNull);
      await store.save(_sample()); // must not throw
    });

    test('explicit cacheKeys isolate tenants sharing one box', () async {
      final box = await freshBox();
      final a = HiveCacheStore(box: box, env: 'dev', cacheKey: 'tenant_a');
      final b = HiveCacheStore(box: box, env: 'dev', cacheKey: 'tenant_b');
      await a.save(_sample());
      expect(await b.load(), isNull);
      expect((await a.load())!.etag, 'etag-abc');
    });

    test('toString is redacted to cacheKey only', () async {
      final s = HiveCacheStore(box: await freshBox(), env: 'dev').toString();
      expect(s, contains('cache_dev'));
      expect(s, isNot(contains('etag-abc')));
      expect(s, isNot(contains('launch_flag')));
    });
  });
}
