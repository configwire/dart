import 'dart:io';

import 'package:configwire/configwire.dart';

/// Defaults demo (cold path) + live-fetch demo (used by T12 live smoke).
///
/// Live mode (env vars; apiKey printed NEVER — values only):
///   CW_BASE_URL=http://127.0.0.1:8102 CW_API_KEY=`<sdk-key>` CW_ENV=dev \
///     CW_CACHE=/tmp/cw-t12-smoke/cache.json dart run example/main.dart
Future<void> main() async {
  final baseUrl = Platform.environment['CW_BASE_URL'] ?? 'http://localhost:8090';
  final apiKey = Platform.environment['CW_API_KEY'] ?? 'demo-key';
  final env = Platform.environment['CW_ENV'] ?? 'dev';
  final cache = Platform.environment['CW_CACHE'];
  final live = Platform.environment['CW_LIVE'] == '1';

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
