import 'dart:io';

import 'package:config_wire/config_wire.dart';

/// Defaults demo (cold path) + live-fetch demo (used by T12 live smoke).
///
/// Live mode (env vars; apiKey printed NEVER — values only):
///   CN_BASE_URL=http://127.0.0.1:8102 CN_API_KEY=`<sdk-key>` CN_ENV=dev \
///     CN_CACHE=/tmp/cn-t12-smoke/cache.json dart run example/main.dart
Future<void> main() async {
  final baseUrl = Platform.environment['CN_BASE_URL'] ?? 'http://localhost:8090';
  final apiKey = Platform.environment['CN_API_KEY'] ?? 'demo-key';
  final env = Platform.environment['CN_ENV'] ?? 'dev';
  final cache = Platform.environment['CN_CACHE'];
  final live = Platform.environment['CN_LIVE'] == '1';

  final cw = ConfigWire(
    apiKey: apiKey,
    env: env,
    baseUrl: baseUrl,
    defaults: {'welcome': 'hello', 'enabled': true, 'launch_flag': false},
    cacheFile: cache,
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
