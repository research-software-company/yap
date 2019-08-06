# Create a package for YAP
import os
import shutil
import tempfile
import glob
import bz2

tmpdir = ""
yap_in_tmpdir = ""
yapdir = ""

def set_directories():
    global tmpdir, yapdir, yap_in_tmpdir
    tmpdir = tempfile.mkdtemp()
    yapdir = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    yap_in_tmpdir = os.path.join(tmpdir, 'src', 'yap')
    print('Temporary package directory is ', tmpdir)

def set_go_path():
    os.environ['GOPATH'] = tmpdir

def copy_yap_content():
    print('Copying yap to temporary package directory')
    os.makedirs(os.path.join(tmpdir, 'src'))
    shutil.copytree(yapdir, yap_in_tmpdir)
    shutil.rmtree(os.path.join(yap_in_tmpdir, '.git')) # Remove the .git folder which shouldn't be pacakged

def uncompress_data_files():
    def uncompress(bz2_file: string):
        b64_file = bz2_file.replace('.bz2', '.b64')

        with open(b64_file, 'wb') as new_file, bz2.BZ2File(bz2_file, 'rb') as file:
            for data in iter(lambda : file.read(100 * 1024), b''):
                new_file.write(data)
        print('Uncompressed ', bz2_file, ' into ', bz64_file)

    print('Uncompressing data files')
    datadir = os.path.join(yap_in_tmpdir, 'data')
    for bz2_file in glob.glob(os.path.join(datadir, '*.bz2')):
        uncompress(bz2_file)

def go_build():
    os.chdir(yap_in_tmpdir)
    os.system('go get .')
    os.system('go build .')

def run():
    global tmpdir

    print('Generating a package for YAP')
    set_directories()
    cwd = os.curdir

    try:
        set_go_path()
        copy_yap_content()
        uncompress_data_files()
        go_build()
        # zip_file = zip_content()
        # add_script(zip_file)

    finally:
        shutil.rmtree(tmpdir)
        os.chdir(cwd)

if __name__=='__main__':
    run()
