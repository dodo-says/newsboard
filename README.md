## About newsboard

It is a fork from https://github.com/robdelacruz/newsboard
We hope to build an anonymous community forum based on this project.

Main changes from upstream:

- using html/templates
- using flags
- dev mode (reload templates on each request)

-------

newsboard is a bulletin board for posting stories and links. Inspired by HackerNews.

- Submit stories and links.
- Reply to stories.
- Multiple users.
- MIT License

## Build and Install

    $ make
    $ ./nb --init

    Run './nb' to start the web service.

newsboard uses a single sqlite3 database file to store all submissions, users, and site settings.

## Screenshots

![newsboard list](screenshots/nb-index.png)
![newsboard item](screenshots/nb-item1.png)

## Contact
    Twitter: @robcomputing
    Source: http://github.com/robdelacruz/newsboard

