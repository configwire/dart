import 'dart:convert';
import 'dart:io';

import 'package:configwire/configwire.dart';

/// Defaults demo (cold path) + live-fetch demo (used by T12 live smoke).
///
/// Caching is bring-your-own via the [CacheStore] seam. Omitting `store:`
/// uses the default session-only [MemoryCacheStore] (no disk). For disk
/// persistence, implement [CacheStore] yourself (example: [_JsonFileStore]
/// below) and pass it as `store:`.
///
/// Live mode (env vars; apiKey printed NEVER — values only):
///   CW_BASE_URL=http://127.0.0.1:8102 CW_API_KEY=`<sdk-key>` CW_ENV=dev \
///     CW_LIVE=1 dart run example/main.dart
Future<void> main() async {
  final baseUrl = Platform.environment['CW_BASE_URL'] ?? 'http://localhost:8090';
  final apiKey = Platform.environment['CW_API_KEY'] ?? 'demo-key';
  final env = Platform.environment['CW_ENV'] ?? 'dev';
  final live = Platform.environment['CW_LIVE'] == '1';

  final cw = ConfigWire(
    apiKey: apiKey,
    env: env,
    baseUrl: baseUrl,
    defaults: {'welcome': 'hello', 'enabled': true, 'launch_flag': false},
    // Omit `store:` for the default session-only memory cache.
    // For disk persistence: `store: _JsonFileStore(env: env),`
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

/// Example disk-backed [CacheStore]: one JSON file per environment.
///
/// Pure `dart:io` + `dart:convert`, no extra dependencies. Load returns
/// null on any miss/corruption (caller falls back to defaults); save
/// overwrites the file.
// ignore: unused_element
class _JsonFileStore implements CacheStore {
  _JsonFileStore({required this.env, String? dir})
      : _file = File('${dir ?? '.configwire-cache'}/cache_$env.json');

  final String env;
  final File _file;

  @override
  Future<CacheData?> load() async {
    try {
      final raw = await _file.readAsString();
      final decoded = jsonDecode(raw);
      if (decoded is! Map) return null;
      return CacheData.fromJson(Map<String, Object?>.from(decoded));
    } catch (_) {
      return null;
    }
  }

  @override
  Future<void> save(CacheData data) async {
    await _file.parent.create(recursive: true);
    await _file.writeAsString(jsonEncode(data.toJson()));
  }
}
