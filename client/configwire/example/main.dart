import 'dart:io';

import 'package:configwire/configwire.dart';
import 'package:hive_ce/hive_ce.dart';

/// Defaults demo (cold path) + live-fetch demo (used by T12 live smoke).
///
/// Bring-your-own-box: the host inits Hive, opens the box, and hands it
/// to `HiveCacheStore`. On Flutter call `Hive.initFlutter()` instead of
/// `Hive.init`; on Web skip init (IndexedDB) and just open the box.
///
/// Live mode (env vars; apiKey printed NEVER — values only):
///   CW_BASE_URL=http://127.0.0.1:8102 CW_API_KEY=`<sdk-key>` CW_ENV=dev \
///     CW_HIVE_DIR=/tmp/cw-t12-smoke/hive dart run example/main.dart
Future<void> main() async {
  final baseUrl = Platform.environment['CW_BASE_URL'] ?? 'http://localhost:8090';
  final apiKey = Platform.environment['CW_API_KEY'] ?? 'demo-key';
  final env = Platform.environment['CW_ENV'] ?? 'dev';
  final hiveDir = Platform.environment['CW_HIVE_DIR'] ?? '.configwire-hive';
  final live = Platform.environment['CW_LIVE'] == '1';

  // Host-owned Hive: init + open, then hand the box to the store.
  Hive.init(hiveDir);
  final box = await Hive.openBox('configwire_cache');

  final cw = ConfigWire(
    apiKey: apiKey,
    env: env,
    baseUrl: baseUrl,
    defaults: {'welcome': 'hello', 'enabled': true, 'launch_flag': false},
    store: HiveCacheStore(box: box, env: env),
    // Dev smoke: no throttle so repeated runs always hit the server.
    minimumFetchInterval: Duration.zero,
  );

  // ignore: avoid_print
  print('welcome=${cw.getString('welcome')} enabled=${cw.getBool('enabled')}');

  if (live) {
    await cw.ensureInitialized();
    // NOTE: values printed, apiKey never printed.
    // ignore: avoid_print
    print(
      'live_fetch status=${cw.lastFetchStatus} version=${cw.version} '
      'etag=${cw.etag} launch_flag=${cw.getBool('launch_flag')} '
      'welcome=${cw.getString('welcome')} variants=${cw.getVariants()}',
    );
  }
  await cw.dispose();
}
